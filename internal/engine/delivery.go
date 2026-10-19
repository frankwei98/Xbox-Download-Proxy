package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"time"
	"xboxdownloadproxy/internal/domain"
	"xboxdownloadproxy/internal/offline"
)

type deliveryWriter struct {
	http.ResponseWriter
	bytes  uint64
	status int
}

func (w *deliveryWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *deliveryWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *deliveryWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += uint64(n)
	return n, err
}

func (r *Runtime) serveLocal(w http.ResponseWriter, req *http.Request, host string) bool {
	if r.files == nil {
		return false
	}
	f, task, err := r.files.OpenResource("http://" + host + req.URL.RequestURI())
	if err != nil {
		return false
	}
	defer f.Close()
	// A complete game can take hours to send. Do not inherit the redirect
	// server's short response budget. Closing the runtime still closes sockets.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-XDP-Source", "local")
	if task.ETag != "" {
		w.Header().Set("ETag", task.ETag)
	}
	written := &deliveryWriter{ResponseWriter: w}
	http.ServeContent(written, req, task.Name, offline.ResourceModTime(task), f)
	if written.status == http.StatusOK || written.status == http.StatusPartialContent {
		r.localHits.Add(1)
		r.localBytes.Add(written.bytes)
	}
	r.log("LOCAL", req.RemoteAddr, host, fmt.Sprintf("本地回传 %s：HTTP %d，已发送 %d 字节", task.Name, written.status, written.bytes), "")
	return true
}

func (r *Runtime) newHTTPTransport() *http.Transport {
	return &http.Transport{
		DisableCompression: true, ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout: 30 * time.Second, MaxIdleConns: 32, MaxIdleConnsPerHost: 4,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil || port != "80" || !domain.LocalDeliveryHost(host) {
				return nil, errors.New("HTTP relay only permits supported Xbox CDN hosts on port 80")
			}
			ip, err := r.TLSUpstream(ctx, host)
			if err != nil {
				return nil, err
			}
			return r.dialHTTP(ctx, network, net.JoinHostPort(ip, "80"))
		},
	}
}

func (r *Runtime) relayHTTP(w http.ResponseWriter, req *http.Request, host string) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	link := ""
	if r.cfg.ShowLinks {
		link = "http://" + host + req.URL.RequestURI()
	}
	r.log("HTTP RELAY", req.RemoteAddr, host, "本地未命中，从 CDN 转发", link)
	proxy := &httputil.ReverseProxy{
		Transport: r.httpTransport,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.Out.URL.Scheme = "http"
			p.Out.URL.Host = host
			p.Out.Host = host
			p.Out.Header.Del("Authorization")
			p.Out.Header.Del("Cookie")
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			r.log("HTTP_ERROR", req.RemoteAddr, host, err.Error(), "")
			http.Error(w, "CDN relay unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, req)
}
