package speedtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		j, _ = m.Get(j.ID)
		if j.State == "cancelled" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cancel did not finish")
}
