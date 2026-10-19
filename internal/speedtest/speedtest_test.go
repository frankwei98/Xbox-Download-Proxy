package speedtest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"xboxdownloadproxy/internal/config"
)

func TestMeasurePinnedIPPreservesHostAndRange(t *testing.T) {
	var gotHost, gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Range", "bytes 0-131071/1048576")
		w.WriteHeader(206)
		w.Write([]byte(strings.Repeat("x", 131072)))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	limits := config.Test{TimeoutSeconds: 2, MaxBytes: 131072, Concurrency: 1}
	res := Measure(context.Background(), "127.0.0.1", "http://assets1.xboxlive.cn:"+u.Port()+"/test", limits)
	if res.Error != "" || res.Bytes != 131072 || res.Mbps <= 0 || gotHost != "assets1.xboxlive.cn:"+u.Port() || gotRange != "bytes=0-131071" {
		t.Fatalf("%+v host=%s range=%s", res, gotHost, gotRange)
	}
	if res.Mbps != float64(res.Bytes)*8/1e6/res.Seconds {
		t.Fatal("throughput unit mismatch")
	}
}
func TestMeasureRejectsRedirectAndInvalidRange(t *testing.T) {
	for _, status := range []int{302, 206} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://example.com")
			w.Header().Set("Content-Range", "bytes 50-100/1000")
			w.WriteHeader(status)
		}))
		u, _ := url.Parse(srv.URL)
		res := Measure(context.Background(), "127.0.0.1", "http://test.example:"+u.Port()+"/file", config.Test{TimeoutSeconds: 1, MaxBytes: 1024})
		srv.Close()
		if res.Error == "" {
			t.Fatalf("accepted %d", status)
		}
	}
}
func TestManagerCancelAndExcludeConcurrentJob(t *testing.T) {
	m := New()
	m.probe = func(ctx context.Context, ip, target string, _ config.Test) Result {
		<-ctx.Done()
		return Result{IP: ip, Error: "cancelled"}
	}
	j, e := m.Start("cn1", "http://assets1.xboxlive.cn/file", []string{"1.1.1.1", "8.8.8.8"}, config.Test{Concurrency: 1})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Start("cn1", j.TestURL, []string{"1.1.1.1"}, config.Test{Concurrency: 1}); e == nil {
		t.Fatal("concurrent job accepted")
	}
	m.Cancel(j.ID)
	if got := waitJob(t, m, j.ID); got.State != "cancelled" {
		t.Fatalf("cancelled job state = %q, want cancelled", got.State)
	}
}

func TestStartValidatesAndDeduplicates(t *testing.T) {
	tests := []struct {
		name, group, target string
		ips                 []string
		want                int
		wantErr             bool
	}{
		{"deduplicates", "cn1", "http://assets1.xboxlive.cn/file", []string{"1.1.1.1", "8.8.8.8", "1.1.1.1"}, 2, false},
		{"maximum candidate count", "cn1", "http://assets1.xboxlive.cn/file", makeIPs(128), 128, false},
		{"empty", "cn1", "http://assets1.xboxlive.cn/file", nil, 0, true},
		{"too many", "cn1", "http://assets1.xboxlive.cn/file", makeIPs(129), 0, true},
		{"private address", "cn1", "http://assets1.xboxlive.cn/file", []string{"127.0.0.1"}, 0, true},
		{"invalid URL", "cn1", "file:///etc/passwd", []string{"1.1.1.1"}, 0, true},
		{"nonstandard port", "cn1", "http://assets1.xboxlive.cn:8080/file", []string{"1.1.1.1"}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New()
			m.probe = func(context.Context, string, string, config.Test) Result { return Result{} }
			j, err := m.Start(tt.group, tt.target, tt.ips, config.Test{Concurrency: 2})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Start() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && j.Total != tt.want {
				t.Fatalf("Start() Total = %d, want %d", j.Total, tt.want)
			}
			m.Close()
		})
	}
}

func awaitProbeSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not reach the expected synchronization point")
	}
}

func makeIPs(n int) []string {
	ips := make([]string, n)
	for i := range ips {
		ips[i] = fmt.Sprintf("8.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256)
	}
	return ips
}

func waitJob(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		job, ok := m.Get(id)
		if !ok {
			t.Fatalf("job %q disappeared", id)
		}
		if job.State != "running" {
			return job
		}
		select {
		case <-deadline.C:
			t.Fatalf("job %q did not finish: %+v", id, job)
		case <-ticker.C:
		}
	}
}

func TestManagerCompletionSortsAndReturnsSnapshot(t *testing.T) {
	m := New()
	var once sync.Once
	allStarted := make(chan struct{})
	release := make(chan struct{})
	var released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); m.Close() })
	m.probe = func(_ context.Context, ip, _ string, _ config.Test) Result {
		// All workers can start; release yields deterministic distinct results.
		once.Do(func() { close(allStarted) })
		<-release
		if ip == "1.1.1.1" {
			return Result{IP: ip, Mbps: 1}
		}
		if ip == "8.8.8.8" {
			return Result{IP: ip, Mbps: 10}
		}
		return Result{IP: ip, Error: "probe failed"}
	}
	j, err := m.Start("cn1", "http://assets1.xboxlive.cn/file", []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}, config.Test{Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	awaitProbeSignal(t, allStarted)
	// The running snapshot owns its Results slice and cannot mutate manager state.
	j.Results = append(j.Results, Result{IP: "injected"})
	unblock()
	finished := waitJob(t, m, j.ID)
	if finished.State != "done" || finished.Completed != 3 || finished.Total != 3 || len(finished.Results) != 3 {
		t.Fatalf("completed job = %+v", finished)
	}
	if finished.Results[0].IP != "8.8.8.8" || finished.Results[1].IP != "1.1.1.1" || finished.Results[2].Error == "" {
		t.Fatalf("unexpected result order: %+v", finished.Results)
	}
	if _, ok := m.Cancel("unknown"); ok {
		t.Fatal("unknown job cancellation succeeded")
	}
	if _, ok := m.Cancel(finished.ID); !ok {
		t.Fatal("completed job cancellation should identify job")
	}
	finished.Results[0].IP = "mutated"
	again, _ := m.Get(j.ID)
	if again.Results[0].IP != "8.8.8.8" {
		t.Fatalf("returned snapshot mutated stored results: %+v", again.Results)
	}
}

func TestManagerLimitsConcurrentProbes(t *testing.T) {
	m := New()
	var active, peak atomic.Int32
	started := make(chan struct{}, 5)
	release := make(chan struct{})
	var released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); m.Close() })
	m.probe = func(_ context.Context, ip, _ string, _ config.Test) Result {
		n := active.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return Result{IP: ip, Mbps: 1}
	}
	j, err := m.Start("cn1", "http://assets1.xboxlive.cn/file", []string{"1.1.1.1", "8.8.8.8", "9.9.9.9", "4.2.2.2", "4.2.2.1"}, config.Test{Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	awaitProbeSignal(t, started)
	awaitProbeSignal(t, started)
	if n := peak.Load(); n != 2 {
		t.Fatalf("peak concurrency = %d, want 2", n)
	}
	unblock()
	finished := waitJob(t, m, j.ID)
	if finished.Completed != 5 || peak.Load() > 2 {
		t.Fatalf("job = %+v, peak concurrency %d", finished, peak.Load())
	}
}

func TestMeasureResponseBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		rangeHeader string
		wantBytes   int64
		wantErr     string
	}{
		{"empty", http.StatusOK, "", "", 0, "empty response"},
		{"HTTP error", http.StatusBadGateway, "failure", "", 0, "HTTP 502"},
		{"caps oversized body", http.StatusOK, strings.Repeat("x", 8192), "", 1024, ""},
		{"truncated range", http.StatusPartialContent, "short", "bytes 0-100/1000", 5, "unexpected EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.rangeHeader != "" {
					w.Header().Set("Content-Range", tt.rangeHeader)
				}
				if tt.name == "truncated range" {
					w.Header().Set("Content-Length", "101")
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			maxBytes := int64(1024)
			if tt.name == "truncated range" {
				maxBytes = 128
			}
			result := Measure(context.Background(), "127.0.0.1", "http://download.example:"+u.Port()+"/file", config.Test{TimeoutSeconds: 2, MaxBytes: maxBytes})
			if result.Bytes != tt.wantBytes {
				t.Fatalf("Bytes = %d, want %d; result %+v", result.Bytes, tt.wantBytes, result)
			}
			if tt.wantErr == "" && result.Error != "" {
				t.Fatalf("unexpected Measure error: %s", result.Error)
			}
			if tt.wantErr != "" && !strings.Contains(result.Error, tt.wantErr) {
				t.Fatalf("Measure error = %q, want substring %q", result.Error, tt.wantErr)
			}
		})
	}
}

func TestMeasureTimeoutAndParentCancellation(t *testing.T) {
	t.Run("timeout below threshold", func(t *testing.T) {
		started := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			w.Header().Set("Content-Length", "131072")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		defer srv.Close()
		u, _ := url.Parse(srv.URL)
		result := Measure(context.Background(), "127.0.0.1", "http://download.example:"+u.Port()+"/file", config.Test{TimeoutSeconds: 1, MaxBytes: 131072})
		awaitProbeSignal(t, started)
		if result.Bytes != 1 || result.Error != "timed out before receiving enough test data" {
			t.Fatalf("timeout result = %+v", result)
		}
	})
	t.Run("parent cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result := Measure(ctx, "127.0.0.1", "http://download.example:80/file", config.Test{TimeoutSeconds: 5, MaxBytes: 131072})
		if result.Error != "cancelled" {
			t.Fatalf("cancellation result = %+v", result)
		}
	})
}

func TestMeasureTimeoutThresholdAt64KiB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "131072")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(strings.Repeat("x", 65536)))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	result := Measure(context.Background(), "127.0.0.1", "http://download.example:"+u.Port()+"/file", config.Test{TimeoutSeconds: 1, MaxBytes: 131072})
	if result.Bytes != 65536 || result.Error != "" {
		t.Fatalf("64 KiB threshold result = %+v", result)
	}
}

func TestMeasureRejectsUntrustedHTTPSCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("8.8.8.8")) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	result := Measure(context.Background(), "127.0.0.1", "https://download.example:"+u.Port()+"/file", config.Test{TimeoutSeconds: 2, MaxBytes: 64})
	if result.Error == "" || !strings.Contains(strings.ToLower(result.Error), "certificate") {
		t.Fatalf("self-signed HTTPS certificate accepted: %+v", result)
	}
}
