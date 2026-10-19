package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"xboxdownloadproxy/internal/config"
	"xboxdownloadproxy/internal/offline"
)

func deliveryFixture(t *testing.T) (*offline.Manager, string, []byte, string) {
	t.Helper()
	dir := t.TempDir()
	id := strings.Repeat("b", 32)
	path := "/01234567-89ab-cdef-0123-456789abcdef/1.2.3.4.abcdef01-2345-6789-abcd-ef0123456789_xs.xvc"
	payload := bytes.Repeat([]byte("downloaded-on-NAS"), 4096)
	task := offline.Task{ID: id, Name: "renamed.game", URL: "http://assets2.xboxlive.cn" + path, OriginalURL: "http://xvcf1.xboxlive.com" + path + "?sig=original", State: "completed", Bytes: int64(len(payload)), TotalBytes: int64(len(payload)), ETag: `"resource-v1"`, LastModified: "Mon, 19 Oct 2026 00:00:00 GMT", CreatedAt: time.Now().UTC()}
	if err := os.MkdirAll(filepath.Join(dir, id), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, id, task.Name)
	if err := os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal([]offline.Task{task})
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := offline.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, path, payload, file
}

func TestXboxHTTPDeliveryRangesDNSAndCDNFallback(t *testing.T) {
	m, path, payload, file := deliveryFixture(t)
	c := testConfig(t)
	c.OfflineEnabled = true
	c.TLSListen = freeTCP(t)
	c.SuppressAAAA = false // Delivery must still suppress IPv6 routes that bypass NAS.
	g := c.Groups["cn1"]
	g.IP = "1.1.1.1"
	c.Groups["cn1"] = g
	r, err := Start(c, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, host, path, rangeHeader string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://"+c.HTTPListen+path, nil)
		req.Host = host
		req.Header.Set("Range", rangeHeader)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return res, data
	}
	for _, tc := range []struct {
		method, rng string
		status      int
		body        []byte
	}{
		{"GET", "", 200, payload}, {"GET", "bytes=3-19", 206, payload[3:20]},
		{"GET", "bytes=-7", 206, payload[len(payload)-7:]}, {"HEAD", "", 200, nil},
	} {
		res, data := request(tc.method, "assets2.xboxlive.cn", path+"?sig=renewed", tc.rng)
		if res.StatusCode != tc.status || !bytes.Equal(data, tc.body) || res.Header.Get("X-XDP-Source") != "local" || res.Header.Get("ETag") != `"resource-v1"` {
			t.Fatalf("%s %s: status=%d bytes=%d headers=%v", tc.method, tc.rng, res.StatusCode, len(data), res.Header)
		}
	}
	res, _ := request("GET", "assets1.xboxlive.com", path, "bytes=99999999-")
	if res.StatusCode != 416 || res.Header.Get("Content-Range") != fmt.Sprintf("bytes */%d", len(payload)) {
		t.Fatal("bad out-of-range response", res.StatusCode, res.Header)
	}
	if got := r.Metrics(); got.LocalHits != 4 || got.LocalBytes < uint64(len(payload)+24) {
		t.Fatalf("missing local metrics %+v", got)
	}
	for _, host := range []string{"assets1.xboxlive.com", "assets2.xboxlive.cn", "dlassets2.xboxlive.cn"} {
		b, err := exchange(context.Background(), c.DNSListen, query(host), false)
		if err != nil || !bytes.Equal(b[len(b)-4:], net.ParseIP(c.LANIP).To4()) {
			t.Fatalf("delivery DNS %s: %x %v", host, b, err)
		}
		q := query(host)
		binary.BigEndian.PutUint16(q[len(q)-4:], 28)
		b, err = exchange(context.Background(), c.DNSListen, q, false)
		if err != nil || binary.BigEndian.Uint16(b[6:8]) != 0 {
			t.Fatal("delivery IPv6 bypass", err)
		}
	}
	res, _ = request("GET", "assets1.xboxlive.com", "/missing?sig=a%2Fb", "")
	if res.StatusCode != 302 || res.Header.Get("Location") != "http://assets2.xboxlive.cn/missing?sig=a%2Fb" {
		t.Fatal("miss lost HTTP CDN redirect", res.StatusCode, res.Header)
	}
	res, _ = request("GET", "assets2.xboxlive.cn.evil.test", path, "")
	if res.StatusCode != 404 {
		t.Fatal("unapproved host served")
	}

	// The terminal CDN alias also resolves to NAS. It must connect to the
	// selected public IP, preserving signed paths and ranges, without a loop.
	var relayed atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		relayed.Add(1)
		if req.Host != "assets2.xboxlive.cn" || req.URL.RawQuery != "sig=a%2Fb" {
			t.Errorf("relay changed source: %s %s", req.Host, req.URL)
		}
		http.ServeContent(w, req, "origin.bin", time.Time{}, bytes.NewReader([]byte("origin-content")))
	}))
	defer origin.Close()
	r.dialHTTP = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "1.1.1.1:80" {
			return nil, fmt.Errorf("unexpected relay address %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
	}
	res, data := request("GET", "assets2.xboxlive.cn", "/missing?sig=a%2Fb", "bytes=2-5")
	if res.StatusCode != 206 || string(data) != "igin" || relayed.Load() != 1 {
		t.Fatal("terminal miss did not relay range", res.StatusCode, string(data))
	}
	if err := os.Truncate(file, 1); err != nil {
		t.Fatal(err)
	}
	res, data = request("GET", "assets2.xboxlive.cn", path+"?sig=a%2Fb", "")
	if res.Header.Get("X-XDP-Source") == "local" || string(data) != "origin-content" {
		t.Fatal("changed local file was served")
	}
}

func TestConcurrentLocalRangesAndDisabledDelivery(t *testing.T) {
	m, path, payload, _ := deliveryFixture(t)
	c := config.Default()
	c.OfflineEnabled = true
	r := &Runtime{cfg: c, files: m, httpSlots: make(chan struct{}, 32), log: func(string, string, string, string, string) {}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "http://assets2.xboxlive.cn"+path, nil)
			req.RemoteAddr = "192.168.1.2:1234"
			req.Header.Set("Range", "bytes=5-99")
			w := httptest.NewRecorder()
			r.redirect(w, req)
			if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), payload[5:100]) {
				t.Errorf("concurrent range: %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if r.Metrics().LocalBytes != 8*95 {
		t.Fatal("concurrent byte accounting")
	}
	r.cfg.OfflineEnabled = false
	req := httptest.NewRequest("GET", "http://assets1.xboxlive.com"+path, nil)
	req.RemoteAddr = "192.168.1.2:1234"
	w := httptest.NewRecorder()
	r.redirect(w, req)
	if w.Code != 302 || w.Header().Get("X-XDP-Source") != "" {
		t.Fatal("disabled feature used local file")
	}
	for _, remote := range []string{"8.8.8.8:1234", "invalid"} {
		req.RemoteAddr = remote
		w = httptest.NewRecorder()
		r.redirect(w, req)
		if w.Code != 403 {
			t.Fatal("non-LAN file access allowed")
		}
	}
	r.cfg.LANIP = "1.1.1.1"
	group := r.cfg.Groups["global"]
	group.IP = r.cfg.LANIP
	r.cfg.Groups["global"] = group
	if _, err := r.TLSUpstream(context.Background(), "assets2.xboxlive.com"); err == nil {
		t.Fatal("selected address can loop back into this listener")
	}
}
