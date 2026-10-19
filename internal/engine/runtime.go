package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"xboxdownloadproxy/internal/config"
	"xboxdownloadproxy/internal/domain"
)

type Logger func(kind, client, host, message, url string)
type Runtime struct {
	cfg            config.Config
	ctx            context.Context
	cancel         context.CancelFunc
	udp            net.PacketConn
	dnsTCP         net.Listener
	httpListener   net.Listener
	tlsListener    net.Listener
	server         *http.Server
	log            Logger
	slots          chan struct{}
	mu             sync.Mutex
	closed         bool
	connections    map[net.Conn]bool
	dnsQueries     atomic.Uint64
	httpRedirects  atomic.Uint64
	tlsConnections atomic.Uint64
	dialTLS        func(context.Context, string, string) (net.Conn, error)
}
type Metrics struct {
	DNSQueries     uint64 `json:"dnsQueries"`
	HTTPRedirects  uint64 `json:"httpRedirects"`
	TLSConnections uint64 `json:"tlsConnections"`
}

func (r *Runtime) Metrics() Metrics {
	return Metrics{r.dnsQueries.Load(), r.httpRedirects.Load(), r.tlsConnections.Load()}
}
func Start(c config.Config, log Logger) (*Runtime, error) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{cfg: config.Clone(c), ctx: ctx, cancel: cancel, log: log, slots: make(chan struct{}, 128), connections: map[net.Conn]bool{}}
	r.dialTLS = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	if log == nil {
		r.log = func(string, string, string, string, string) {}
	}
	var e error
	if r.udp, e = net.ListenPacket("udp", c.DNSListen); e != nil {
		r.Close()
		return nil, fmt.Errorf("DNS UDP %s: %w", c.DNSListen, e)
	}
	if r.dnsTCP, e = net.Listen("tcp", c.DNSListen); e != nil {
		r.Close()
		return nil, fmt.Errorf("DNS TCP %s: %w", c.DNSListen, e)
	}
	if r.httpListener, e = net.Listen("tcp", c.HTTPListen); e != nil {
		r.Close()
		return nil, fmt.Errorf("HTTP %s: %w", c.HTTPListen, e)
	}
	if c.TLSListen != "" {
		if r.tlsListener, e = net.Listen("tcp", c.TLSListen); e != nil {
			r.Close()
			return nil, fmt.Errorf("TLS %s: %w", c.TLSListen, e)
		}
	}
	r.server = &http.Server{Handler: http.HandlerFunc(r.redirect), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16 * 1024}
	go r.serveUDP()
	go r.serveDNSTCP()
	go func() {
		e := r.server.Serve(r.httpListener)
		if e != nil && !errors.Is(e, http.ErrServerClosed) {
			r.log("ERROR", "", "", e.Error(), "")
		}
	}()
	if r.tlsListener != nil {
		go r.serveTLS()
	}
	return r, nil
}
func localClient(addr string) bool {
	host, _, e := net.SplitHostPort(addr)
	if e != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}
func (r *Runtime) admit(c net.Conn) bool {
	if !localClient(c.RemoteAddr().String()) {
		c.Close()
		return false
	}
	select {
	case r.slots <- struct{}{}:
	default:
		c.Close()
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		<-r.slots
		c.Close()
		return false
	}
	r.connections[c] = true
	return true
}
func (r *Runtime) release(c net.Conn) {
	c.Close()
	r.mu.Lock()
	delete(r.connections, c)
	r.mu.Unlock()
	<-r.slots
}
func (r *Runtime) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.cancel()
	for c := range r.connections {
		c.Close()
	}
	r.mu.Unlock()
	if r.server != nil {
		_ = r.server.Close()
	}
	if r.udp != nil {
		_ = r.udp.Close()
	}
	if r.dnsTCP != nil {
		_ = r.dnsTCP.Close()
	}
	if r.httpListener != nil {
		_ = r.httpListener.Close()
	}
	if r.tlsListener != nil {
		_ = r.tlsListener.Close()
	}
}
func (r *Runtime) redirect(w http.ResponseWriter, req *http.Request) {
	if !localClient(req.RemoteAddr) {
		http.Error(w, "LAN clients only", http.StatusForbidden)
		return
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", 405)
		return
	}
	host := req.Host
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	host = domain.Normalize(host)
	dest := domain.Redirect(r.cfg, host)
	if dest == "" {
		http.NotFound(w, req)
		return
	}
	location := "http://" + dest + req.URL.RequestURI()
	w.Header().Set("Location", location)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
	r.httpRedirects.Add(1)
	link := ""
	if r.cfg.ShowLinks {
		link = "http://" + host + req.URL.RequestURI()
	}
	r.log("HTTP 302", req.RemoteAddr, host, "重定向到 "+dest, link)
}
func (r *Runtime) TLSUpstream(ctx context.Context, host string) (string, error) {
	key := domain.GroupFor(host)
	if key == "" {
		return "", errors.New("SNI is not an allowed download domain")
	}
	if ip := r.cfg.Groups[key].IP; ip != "" {
		return ip, nil
	}
	ips, e := ResolveIPv4(ctx, host, r.cfg.UpstreamDNS)
	if e != nil {
		return "", e
	}
	for _, ip := range ips {
		if ip != r.cfg.LANIP {
			return ip, nil
		}
	}
	return "", errors.New("upstream DNS points back to the service")
}
func CleanHost(host string) string { return strings.TrimSuffix(strings.ToLower(host), ".") }
