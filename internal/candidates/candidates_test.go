package candidates

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"xboxdownloadproxy/internal/config"
)

func TestSourcesCacheAndImport(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("XboxCn1\n1.1.1.1 中国 电信\n1.1.1.1 duplicate\n192.168.1.1 rejected\n"))
	}))
	defer srv.Close()
	s := New(t.TempDir())
	s.Client = srv.Client()
	c := config.Default()
	g := c.Groups["cn1"]
	g.SourceURL = srv.URL
	c.Groups["cn1"] = g
	l, e := s.Load(context.Background(), c, "cn1", false)
	if e != nil {
		t.Fatal(e)
	}
	if l.Origin != "online" || len(l.Items) != 1 || l.Items[0].Location != "中国 电信" {
		t.Fatalf("%+v", l)
	}
	srv.Close()
	l, e = s.Load(context.Background(), c, "cn1", true)
	if e != nil || l.Origin != "cache" || l.Warning == "" {
		t.Fatalf("offline fallback: %+v %v", l, e)
	}
	l, e = s.Import("store", "8.8.8.8 imported")
	if e != nil {
		t.Fatal(e)
	}
	l, e = s.Load(context.Background(), c, "store", false)
	if e != nil || l.Items[0].IP != "8.8.8.8" {
		t.Fatalf("import cache lost: %+v %v", l, e)
	}
	if _, e = s.Import("../escape", "1.1.1.1"); e == nil {
		t.Fatal("path traversal group accepted")
	}
	if _, e = Parse("127.0.0.1\nnot-an-ip\n169.254.1.1"); e == nil {
		t.Fatal("private IP list accepted")
	}
}

func TestParseBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []Item
		wantErr bool
	}{
		{name: "blank and comments", input: " \n# comment\n#1.1.1.1 ignored\n8.8.8.8\n", want: []Item{{IP: "8.8.8.8"}}},
		{name: "empty", input: "", wantErr: true},
		{name: "comments only", input: "# list unavailable\n  \n", wantErr: true},
		{name: "whitespace and location", input: "  1.1.1.1   中国 电信  \n", want: []Item{{IP: "1.1.1.1", Location: "中国 电信"}}},
		{name: "duplicate keeps first location", input: "8.8.8.8 first\n8.8.8.8 second\n", want: []Item{{IP: "8.8.8.8", Location: "first"}}},
		{name: "reject private and IPv6", input: "192.168.1.2\n2001:4860:4860::8888 ipv6\n", wantErr: true},
		{name: "reject malformed", input: "not-an-ip example\n", wantErr: true},
		{name: "scanner line limit", input: "8.8.8.8 " + strings.Repeat("x", 65536), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Parse() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseMaximumCandidates(t *testing.T) {
	var input strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&input, "8.%d.%d.%d\n", (i/65536)%256, (i/256)%256, i%256)
	}
	items, err := Parse(input.String())
	if err != nil || len(items) != 20000 {
		t.Fatalf("Parse(20000) returned %d items, %v", len(items), err)
	}
	input.WriteString("9.9.9.9\n")
	if _, err := Parse(input.String()); err == nil || !strings.Contains(err.Error(), "exceeds 20000") {
		t.Fatalf("Parse(20001) error = %v", err)
	}
}

func TestLoadFreshCacheAndRefreshRules(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte("1.1.1.1 source"))
	}))
	defer srv.Close()
	s := New(t.TempDir())
	s.Client = srv.Client()
	c := config.Default()
	g := c.Groups["cn1"]
	g.SourceURL = srv.URL
	c.Groups["cn1"] = g
	first, err := s.Load(context.Background(), c, "cn1", false)
	if err != nil || first.Origin != "online" || calls.Load() != 1 {
		t.Fatalf("first Load() = %+v, %v (calls %d)", first, err, calls.Load())
	}
	cached, err := s.Load(context.Background(), c, "cn1", false)
	if err != nil || cached.Origin != "cache" || calls.Load() != 1 {
		t.Fatalf("fresh cache Load() = %+v, %v (calls %d)", cached, err, calls.Load())
	}
	changedCalls := atomic.Int32{}
	changedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		changedCalls.Add(1)
		w.Write([]byte("9.9.9.9 changed"))
	}))
	defer changedSrv.Close()
	changed := config.Clone(c)
	g = changed.Groups["cn1"]
	g.SourceURL = changedSrv.URL
	changed.Groups["cn1"] = g
	updated, err := s.Load(context.Background(), changed, "cn1", false)
	if err != nil || updated.Origin != "online" || updated.Items[0].IP != "9.9.9.9" || updated.SourceURL != changedSrv.URL {
		t.Fatalf("source change Load() = %+v, %v", updated, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("unexpected old source calls: %d", calls.Load())
	}
	forced, err := s.Load(context.Background(), changed, "cn1", true)
	if err != nil || forced.Origin != "online" || forced.Items[0].IP != "9.9.9.9" || changedCalls.Load() != 2 {
		t.Fatalf("forced refresh = %+v, %v (calls %d)", forced, err, changedCalls.Load())
	}
}

func TestLoadExpiredCacheFallsBackWithWarning(t *testing.T) {
	s := New(t.TempDir())
	c := config.Default()
	g := c.Groups["cn1"]
	g.SourceURL = "http://127.0.0.1:1/offline"
	c.Groups["cn1"] = g
	old := List{Group: "cn1", Items: []Item{{IP: "8.8.8.8"}}, Origin: "online", SourceURL: g.SourceURL, UpdatedAt: time.Now().Add(-25 * time.Hour)}
	if err := s.save(old); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(context.Background(), c, "cn1", false)
	if err != nil || got.Origin != "cache" || got.Warning == "" || len(got.Items) != 1 || got.Items[0].IP != "8.8.8.8" {
		t.Fatalf("expired cache fallback = %+v, %v", got, err)
	}
}

func TestDownloadErrorsAndLimits(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"http status", http.StatusServiceUnavailable, "", "HTTP 503"},
		{"oversized", http.StatusOK, strings.Repeat("x", 4*1024*1024+1), "exceeds 4MiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			s := New(t.TempDir())
			s.Client = srv.Client()
			if _, err := s.download(context.Background(), srv.URL, "cn1"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("download error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadCancellationDoesNotUseFallback(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(stopped) }))
	defer srv.Close()
	s := New(t.TempDir())
	s.Client = srv.Client()
	c := config.Default()
	g := c.Groups["cn1"]
	g.SourceURL = srv.URL
	c.Groups["cn1"] = g
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Load(ctx, c, "cn1", false); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("source request never reached the fixture")
	}
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled source request did not return")
	}
	if err != context.Canceled {
		t.Fatalf("Load() error = %v, want context.Canceled", err)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled request left the source connection running")
	}
}

func TestNewClientRedirectPolicy(t *testing.T) {
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("8.8.8.8 secure")) }))
	defer secure.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, secure.URL, http.StatusFound) }))
	defer plain.Close()
	s := New(t.TempDir())
	// Trust only the local fixture certificate; keep New's redirect callback intact.
	s.Client.Transport = secure.Client().Transport
	got, err := s.download(context.Background(), plain.URL, "cn1")
	if err != nil || got.Items[0].IP != "8.8.8.8" {
		t.Fatalf("HTTP to HTTPS redirect = %+v, %v", got, err)
	}

	downgrade := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, plain.URL, http.StatusFound) }))
	defer downgrade.Close()
	client := secure.Client()
	s.Client = New(t.TempDir()).Client
	s.Client.Transport = client.Transport
	if _, err := s.download(context.Background(), downgrade.URL, "cn1"); err == nil || !strings.Contains(err.Error(), "redirect rejected") {
		t.Fatalf("HTTPS downgrade error = %v", err)
	}

	loop := httptest.NewTLSServer(nil)
	defer loop.Close()
	loop.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, loop.URL, http.StatusFound) })
	s.Client = New(t.TempDir()).Client
	s.Client.Transport = secure.Client().Transport
	if _, err := s.download(context.Background(), loop.URL, "cn1"); err == nil || !strings.Contains(err.Error(), "redirect rejected") {
		t.Fatalf("redirect loop error = %v", err)
	}
}

func TestLoadDNSFallbackSuccessAndFailure(t *testing.T) {
	dns, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer dns.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 512)
		n, addr, e := dns.ReadFromUDP(buf)
		if e != nil {
			return
		}
		query := append([]byte(nil), buf[:n]...)
		response := make([]byte, len(query)+16)
		copy(response, query)
		binary.BigEndian.PutUint16(response[2:4], 0x8180)
		binary.BigEndian.PutUint16(response[6:8], 1)
		response = response[:len(query)]
		response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 8, 8, 4, 4)
		_, _ = dns.WriteToUDP(response, addr)
	}()
	s := New(t.TempDir())
	c := config.Default()
	c.UpstreamDNS = dns.LocalAddr().String()
	g := c.Groups["cn1"]
	g.SourceURL = ""
	c.Groups["cn1"] = g
	got, err := s.Load(context.Background(), c, "cn1", false)
	if err != nil || got.Origin != "dns" || len(got.Items) != 1 || got.Items[0].IP != "8.8.4.4" {
		t.Fatalf("DNS fallback = %+v, %v", got, err)
	}
	<-done
	c.UpstreamDNS = "127.0.0.1:0"
	if _, err := New(t.TempDir()).Load(context.Background(), c, "cn1", false); err == nil || !strings.Contains(err.Error(), "DNS fallback failed") {
		t.Fatalf("DNS failure error = %v", err)
	}
}
