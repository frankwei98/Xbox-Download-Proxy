package offline

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDownloadRejectsPrivateDNSAnswersBeforeDialing(t *testing.T) {
	for _, answers := range [][]string{
		{"127.0.0.1"}, {"192.168.1.1"}, {"169.254.169.254"}, {"100.64.0.1"}, {"::ffff:127.0.0.1"}, {"fc00::1"}, {"not-an-ip"}, nil,
	} {
		t.Run(strings.Join(answers, ","), func(t *testing.T) {
			m, err := New(t.TempDir(), func(context.Context, string) ([]string, error) { return answers, nil })
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			var calls atomic.Int32
			m.dial = func(context.Context, string, string) (net.Conn, error) {
				calls.Add(1)
				return nil, errors.New("unsafe dial")
			}
			task, err := m.Add("http://cdn.example/file", "")
			if err != nil {
				t.Fatal(err)
			}
			final := await(t, m, task.ID, func(task Task) bool { return task.State == "failed" || task.State == "completed" })
			if final.State != "failed" || !strings.Contains(final.Error, "no public CDN address") || final.Bytes != 0 || calls.Load() != 0 {
				t.Fatalf("unsafe DNS result: %+v, dial attempts=%d", final, calls.Load())
			}
		})
	}
}

func TestDownloadTriesNextPublicCDNAfterConnectionFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "fallback-file") }))
	t.Cleanup(server.Close)
	m := fixtureManager(t, t.TempDir(), server)
	m.resolve = func(context.Context, string) ([]string, error) {
		return []string{"192.168.1.1", "invalid", "1.1.1.1", "8.8.8.8"}, nil
	}
	attempts := make(chan string, 2)
	m.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		attempts <- addr
		if addr == "1.1.1.1:80" {
			return nil, errors.New("first CDN unavailable")
		}
		if addr != "8.8.8.8:80" {
			return nil, errors.New("unexpected address: " + addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	task, err := m.Add("http://cdn.example/file", "")
	if err != nil {
		t.Fatal(err)
	}
	final := await(t, m, task.ID, func(task Task) bool { return task.State == "failed" || task.State == "completed" })
	if final.State != "completed" || final.Bytes != 13 || len(attempts) != 2 || <-attempts != "1.1.1.1:80" || <-attempts != "8.8.8.8:80" {
		t.Fatalf("CDN fallback: %+v", final)
	}
}

func TestDownloadRejectsUntrustedTLSCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "must not be accepted") }))
	t.Cleanup(server.Close)
	m, err := New(t.TempDir(), func(context.Context, string) ([]string, error) { return []string{"1.1.1.1"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	m.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "1.1.1.1:443" {
			return nil, errors.New("HTTPS selected address changed: " + addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	task, err := m.Add("https://cdn.example/file", "")
	if err != nil {
		t.Fatal(err)
	}
	final := await(t, m, task.ID, func(task Task) bool { return task.State == "failed" || task.State == "completed" })
	if final.State != "failed" || !strings.Contains(final.Error, "certificate") || final.Bytes != 0 {
		t.Fatalf("TLS certificate verification bypassed: %+v", final)
	}
}

func TestDiskReserveExactBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		available uint64
		want      string
	}{
		{"below reserve", ReserveBytes - 1, "paused"},
		{"no room above reserve", ReserveBytes, "paused"},
		{"one byte short", ReserveBytes + 6, "paused"},
		{"exact fit preserves reserve", ReserveBytes + 7, "completed"},
		{"one byte spare", ReserveBytes + 8, "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "payload") }))
			t.Cleanup(server.Close)
			m := fixtureManager(t, t.TempDir(), server)
			m.space = func(string) (Storage, error) { return Storage{AvailableBytes: tc.available}, nil }
			task, err := m.Add("http://cdn.example/file", "")
			if err != nil {
				t.Fatal(err)
			}
			final := await(t, m, task.ID, func(task Task) bool { return task.State != "running" && task.State != "queued" })
			if final.State != tc.want || (tc.want == "completed" && final.Bytes != 7) || (tc.want == "paused" && (final.Bytes != 0 || !strings.Contains(final.Error, "disk space"))) {
				t.Fatalf("reserve boundary: %+v, want %s", final, tc.want)
			}
		})
	}
}
