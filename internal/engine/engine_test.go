package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"xboxdownloadproxy/internal/config"
)

func freeTCP(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	s := l.Addr().String()
	l.Close()
	return s
}
func testConfig(t *testing.T) config.Config {
	c := config.Default()
	c.LANIP = "127.0.0.1"
	c.DNSListen = freeTCP(t)
	c.HTTPListen = freeTCP(t)
	c.TLSListen = ""
	c.WebListen = freeTCP(t)
	return c
}
func TestDNSHTTPAndRollback(t *testing.T) {
	c := testConfig(t)
	r, e := Start(c, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	req := query("assets1.xboxlive.com")
	for _, tcp := range []bool{false, true} {
		b, e := exchange(context.Background(), c.DNSListen, req, tcp)
		if e != nil {
			t.Fatal(e)
		}
		if binary.BigEndian.Uint16(b[6:8]) != 1 || !bytes.Equal(b[len(b)-4:], net.ParseIP(c.LANIP).To4()) {
			t.Fatalf("wrong DNS A answer %x", b)
		}
	}
	aaaa := append([]byte(nil), req...)
	binary.BigEndian.PutUint16(aaaa[len(aaaa)-4:], 28)
	b, e := exchange(context.Background(), c.DNSListen, aaaa, false)
	if e != nil || binary.BigEndian.Uint16(b[6:8]) != 0 || b[3]&15 != 0 {
		t.Fatalf("AAAA: %x, %v", b, e)
	}
	client := http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	hreq, _ := http.NewRequest("GET", "http://"+c.HTTPListen+"/download/file?signature=a%2Fb&offset=1", nil)
	hreq.Host = "assets1.xboxlive.com"
	res, e := client.Do(hreq)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	want := "http://assets2.xboxlive.cn/download/file?signature=a%2Fb&offset=1"
	if res.StatusCode != 302 || res.Header.Get("Location") != want {
		t.Fatalf("redirect %s %q", res.Status, res.Header.Get("Location"))
	}
	hreq.Host = "evil.test"
	res, e = client.Do(hreq)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("unexpected open proxy: %s", res.Status)
	}
	r.Close()
	tcp, e := net.Listen("tcp", c.HTTPListen)
	if e != nil {
		t.Fatal(e)
	}
	defer tcp.Close()
	if failed, e := Start(c, nil, nil); e == nil {
		failed.Close()
		t.Fatal("expected occupied HTTP failure")
	}
	u, e := net.ListenPacket("udp", c.DNSListen)
	if e != nil {
		t.Fatalf("DNS left running after failed startup: %v", e)
	}
	u.Close()
	l, e := net.Listen("tcp", c.DNSListen)
	if e != nil {
		t.Fatal(e)
	}
	l.Close()
}
func TestDNSForwardUDP(t *testing.T) {
	upstream, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer upstream.Close()
	go func() {
		b := make([]byte, 4096)
		n, addr, e := upstream.ReadFrom(b)
		if e != nil {
			return
		}
		q, e := parseQuestion(b[:n])
		if e != nil {
			return
		}
		_, _ = upstream.WriteTo(reply(b[:n], q, net.IPv4(1, 1, 1, 1), 0), addr)
	}()
	c := testConfig(t)
	c.UpstreamDNS = upstream.LocalAddr().String()
	r, e := Start(c, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	b, e := exchange(context.Background(), c.DNSListen, query("example.com"), false)
	if e != nil || !bytes.Equal(b[len(b)-4:], []byte{1, 1, 1, 1}) {
		t.Fatalf("forward %x %v", b, e)
	}
}
func TestClientHelloPreservesWire(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = a.SetDeadline(time.Now().Add(time.Second))
	_ = b.SetDeadline(time.Now().Add(time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		client := tls.Client(a, &tls.Config{ServerName: "assets1.xboxlive.com", MinVersion: tls.VersionTLS12})
		_ = client.Handshake()
	}()
	host, wire, e := clientHello(b)
	if e != nil {
		t.Fatal(e)
	}
	if host != "assets1.xboxlive.com" || len(wire) < 100 {
		t.Fatalf("bad hello: %s %d", host, len(wire))
	}
	host2, wire2, e := clientHello(bytes.NewReader(wire))
	if e != nil || host2 != host || !bytes.Equal(wire, wire2) {
		t.Fatal("wire changed")
	}
	b.Close()
	<-done
	if _, _, e := clientHello(bytes.NewReader([]byte("GET / HTTP/1.1\r\n"))); e == nil {
		t.Fatal("accepted non-TLS")
	}
}
func TestMalformedDNSNames(t *testing.T) {
	for _, b := range [][]byte{{0xc0, 0}, {64, 1}, {63, 1, 2}, {0xc0, 255}, {1, 'a'}} {
		if _, _, e := readName(b, 0); e == nil {
			t.Errorf("accepted malformed name %x", b)
		}
	}
}
func FuzzDNSPacket(f *testing.F) {
	f.Add(query("assets1.xboxlive.com"))
	f.Add([]byte{0xc0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		q, e := parseQuestion(b)
		if e == nil {
			out := reply(b, q, net.IPv4(1, 1, 1, 1), 0)
			if len(out) < 12 {
				t.Fatal("short reply")
			}
		}
	})
}
func TestTLSRejectsUnknownSNI(t *testing.T) {
	r := &Runtime{cfg: testConfig(t)}
	if _, e := r.TLSUpstream(context.Background(), "example.com"); e == nil {
		t.Fatal("open TLS relay")
	}
}

func TestTLSTunnelPreservesCertificateAndHTTPBytes(t *testing.T) {
	seenSNI := make(chan string, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seenSNI <- req.TLS.ServerName
		_, _ = w.Write([]byte("encrypted-download-bytes"))
	}))
	defer upstream.Close()
	c := testConfig(t)
	g := c.Groups["global"]
	g.IP = "1.1.1.1"
	c.Groups["global"] = g
	clientSide, proxySide := net.Pipe()
	r := &Runtime{cfg: c, ctx: context.Background(), log: func(string, string, string, string, string) {}, slots: make(chan struct{}, 1), connections: map[net.Conn]bool{proxySide: true}}
	r.slots <- struct{}{}
	r.dialTLS = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "1.1.1.1:443" {
			t.Errorf("wrong selected CDN %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	done := make(chan struct{})
	go func() { r.tunnel(proxySide); close(done) }()
	// The fixture certificate is for example.com. Compare its exact bytes after
	// the handshake to prove the proxy has not generated a replacement certificate.
	client := tls.Client(clientSide, &tls.Config{ServerName: "assets1.xboxlive.com", InsecureSkipVerify: true})
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if e := client.Handshake(); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(client.ConnectionState().PeerCertificates[0].Raw, upstream.Certificate().Raw) {
		t.Fatal("proxy replaced certificate")
	}
	_, e := io.WriteString(client, "GET /file HTTP/1.1\r\nHost: assets1.xboxlive.com\r\nConnection: close\r\n\r\n")
	if e != nil {
		t.Fatal(e)
	}
	res, e := http.ReadResponse(bufio.NewReader(client), nil)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(res.Body)
	res.Body.Close()
	if e != nil || string(b) != "encrypted-download-bytes" {
		t.Fatalf("body %q %v", b, e)
	}
	if got := <-seenSNI; got != "assets1.xboxlive.com" {
		t.Fatal(got)
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("tunnel did not close")
	}
}
