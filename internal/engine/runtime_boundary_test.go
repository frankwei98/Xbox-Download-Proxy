package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStartupFailureRollsBackEveryEarlierListener(t *testing.T) {
	for _, stage := range []string{"DNS UDP", "DNS TCP", "HTTP", "TLS"} {
		t.Run(stage, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.TLSListen = freeTCP(t)
			blocked := cfg.DNSListen
			if stage == "HTTP" {
				blocked = cfg.HTTPListen
			} else if stage == "TLS" {
				blocked = cfg.TLSListen
			}
			network := "tcp"
			var occupied io.Closer
			var err error
			if stage == "DNS UDP" {
				network = "udp"
				occupied, err = net.ListenPacket(network, blocked)
			} else {
				occupied, err = net.Listen(network, blocked)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer occupied.Close()
			r, err := Start(cfg, nil, nil)
			if r != nil || err == nil || !strings.Contains(err.Error(), stage) {
				if r != nil {
					r.Close()
				}
				t.Fatalf("startup = %v, %v; want %s failure", r, err, stage)
			}
			for _, address := range []struct{ network, addr string }{{"udp", cfg.DNSListen}, {"tcp", cfg.DNSListen}, {"tcp", cfg.HTTPListen}, {"tcp", cfg.TLSListen}} {
				if address.network == network && address.addr == blocked {
					continue
				}
				var listener io.Closer
				if address.network == "udp" {
					listener, err = net.ListenPacket("udp", address.addr)
				} else {
					listener, err = net.Listen("tcp", address.addr)
				}
				if err != nil {
					t.Fatalf("failed startup left %s %s open: %v", address.network, address.addr, err)
				}
				listener.Close()
			}
		})
	}
}

func TestCloseDisconnectsIdleDNSAndTLSClientsAndAllowsRestart(t *testing.T) {
	cfg := testConfig(t)
	cfg.TLSListen = freeTCP(t)
	r, err := Start(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var clients []net.Conn
	for _, addr := range []string{cfg.DNSListen, cfg.TLSListen} {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		clients = append(clients, conn)
		// An incomplete protocol header keeps the connection open before shutdown.
		if _, err := conn.Write([]byte{0}); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	r.Close()
	for _, conn := range clients {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var data [1]byte
		if _, err := conn.Read(data[:]); err == nil {
			t.Fatal("client remained readable after shutdown")
		} else if e, ok := err.(net.Error); ok && e.Timeout() {
			t.Fatal("shutdown did not disconnect a protocol client")
		}
	}
	restarted, err := Start(cfg, nil, nil)
	if err != nil {
		t.Fatal("restart after close:", err)
	}
	restarted.Close()
}

func TestHTTPRelayPreservesResourceButRemovesManagementCredentials(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Host != "assets2.xboxlive.cn" || req.URL.EscapedPath() != "/a%2Fb/file" || req.URL.RawQuery != "sig=a%2Fb%3D&part=2" || req.Header.Get("Range") != "bytes=2-5" {
			t.Errorf("relay changed download request: host=%s url=%s headers=%v", req.Host, req.URL, req.Header)
		}
		if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
			t.Error("management credentials reached the CDN")
		}
		w.Header().Set("Content-Range", "bytes 2-5/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "cdef")
	}))
	defer origin.Close()
	cfg := testConfig(t)
	cfg.OfflineEnabled = true
	group := cfg.Groups["cn1"]
	group.IP = "1.1.1.1"
	cfg.Groups["cn1"] = group
	r := &Runtime{cfg: cfg, ctx: context.Background(), httpSlots: make(chan struct{}, 1), log: func(string, string, string, string, string) {}}
	r.dialHTTP = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "1.1.1.1:80" {
			return nil, errors.New("wrong selected CDN: " + addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
	}
	r.httpTransport = r.newHTTPTransport()
	defer r.httpTransport.CloseIdleConnections()
	request := func() *http.Request {
		req := httptest.NewRequest("GET", "http://assets2.xboxlive.cn/a%2Fb/file?sig=a%2Fb%3D&part=2", nil)
		req.RemoteAddr = "192.168.1.2:1234"
		req.Header.Set("Authorization", "Bearer management-secret")
		req.Header.Set("Cookie", "private=value")
		req.Header.Set("Range", "bytes=2-5")
		return req
	}
	w := httptest.NewRecorder()
	r.redirect(w, request())
	if w.Code != http.StatusPartialContent || w.Body.String() != "cdef" || w.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("relay response = %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	r.httpSlots <- struct{}{}
	w = httptest.NewRecorder()
	r.redirect(w, request())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("saturated relay status = %d", w.Code)
	}
	<-r.httpSlots
	r.httpTransport.CloseIdleConnections()
	r.dialHTTP = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("fixture CDN unavailable")
	}
	w = httptest.NewRecorder()
	r.redirect(w, request())
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "CDN relay unavailable") {
		t.Fatalf("failed relay response = %d %q", w.Code, w.Body.String())
	}
}

func TestHTTPDownloadEntryRejectsUnsupportedMethodsAndHosts(t *testing.T) {
	cfg := testConfig(t)
	r := &Runtime{cfg: cfg, log: func(string, string, string, string, string) {}}
	for _, tc := range []struct {
		method, host string
		want         int
	}{
		{"POST", "assets1.xboxlive.com", 405},
		{"CONNECT", "assets1.xboxlive.com", 405},
		{"GET", "unknown.example", 404},
		{"GET", "dl.delivery.mp.microsoft.com", 404},
		{"HEAD", "ASSETS1.XBOXLIVE.COM.:80", 302},
	} {
		req := httptest.NewRequest(tc.method, "http://localhost/resource?sig=a%2Fb", nil)
		req.Host, req.RemoteAddr = tc.host, "127.0.0.1:1234"
		w := httptest.NewRecorder()
		r.redirect(w, req)
		if w.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.host, w.Code, tc.want)
		}
		if tc.want == 405 && w.Header().Get("Allow") != "GET, HEAD" {
			t.Error("method rejection omitted allowed methods")
		}
	}
}
