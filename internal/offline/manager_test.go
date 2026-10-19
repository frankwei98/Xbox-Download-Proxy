package offline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureManager(t *testing.T, dir string, server *httptest.Server) *Manager {
	t.Helper()
	m, err := New(dir, func(context.Context, string) ([]string, error) { return []string{"1.1.1.1"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	m.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "1.1.1.1:80" {
			return nil, fmt.Errorf("unexpected selected CDN %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	m.space = func(string) (Storage, error) {
		return Storage{Path: dir, TotalBytes: 1 << 40, AvailableBytes: 1 << 40, ReserveBytes: ReserveBytes}, nil
	}
	t.Cleanup(m.Close)
	return m
}
func await(t *testing.T, m *Manager, id string, predicate func(Task) bool) Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, task := range m.List() {
			if task.ID == id && predicate(task) {
				return task
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("task did not reach expected condition: %+v", m.List())
	return Task{}
}

type slowWriter struct{ http.ResponseWriter }

func (w slowWriter) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		n := min(len(b), 4096)
		written, err := w.ResponseWriter.Write(b[:n])
		total += written
		if err != nil {
			return total, err
		}
		w.ResponseWriter.(http.Flusher).Flush()
		b = b[n:]
		time.Sleep(time.Millisecond)
	}
	return total, nil
}

func TestPauseRestartResumePreservesPayloadAndRoute(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), 128*1024)
	var resumed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "cdn.example" || r.URL.RawQuery != "sig=a%2Fb%3D&x=1" {
			t.Errorf("Host or signature changed: %s %s", r.Host, r.URL.String())
		}
		if r.Header.Get("Range") != "" {
			if r.Header.Get("If-Range") != `"version1"` {
				t.Error("missing validator")
			}
			resumed.Store(true)
		}
		w.Header().Set("ETag", `"version1"`)
		http.ServeContent(slowWriter{w}, r, "game.bin", time.Time{}, bytes.NewReader(payload))
	}))
	defer server.Close()
	dir := t.TempDir()
	m := fixtureManager(t, dir, server)
	task, err := m.Add("http://cdn.example/game.bin?sig=a%2Fb%3D&x=1", "game.bin")
	if err != nil {
		t.Fatal(err)
	}
	await(t, m, task.ID, func(t Task) bool { return t.Bytes >= 32*1024 })
	paused, err := m.Action(task.ID, "pause")
	if err != nil || paused.State != "paused" || paused.Bytes == 0 || paused.Bytes >= int64(len(payload)) {
		t.Fatalf("pause %+v %v", paused, err)
	}
	if _, _, err := m.OpenCompleted(task.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("partial file can be served")
	}
	m.Close()
	m = fixtureManager(t, dir, server)
	if tasks := m.List(); len(tasks) != 1 || tasks[0].State != "paused" || tasks[0].Bytes != paused.Bytes {
		t.Fatalf("restart: %+v", tasks)
	}
	if _, err := m.Action(task.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	complete := await(t, m, task.ID, func(t Task) bool { return t.State == "completed" || t.State == "failed" })
	if complete.State != "completed" || !resumed.Load() {
		t.Fatalf("resume %+v", complete)
	}
	f, _, err := m.OpenCompleted(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	f.Close()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("resumed data differs from source")
	}
	s, err := m.Storage()
	if err != nil || s.DownloadBytes != uint64(len(payload)) {
		t.Fatalf("storage %+v %v", s, err)
	}
	if err := m.Delete(task.ID); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 0 {
		t.Fatal("deleted task remains")
	}
	if _, err := os.Stat(dir + "/" + task.ID); !os.IsNotExist(err) {
		t.Fatalf("file directory remains: %v", err)
	}
}

func TestQueueCancelAndShutdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	m := fixtureManager(t, t.TempDir(), server)
	var ids []string
	for range 3 {
		task, err := m.Add("http://cdn.example/file", "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	list := m.List()
	if list[0].State != "running" || list[1].State != "running" || list[2].State != "queued" {
		t.Fatalf("queue %+v", list)
	}
	if err := m.Delete(ids[0]); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted running task")
	}
	if task, err := m.Action(ids[2], "cancel"); err != nil || task.State != "cancelled" {
		t.Fatal(task, err)
	}
	if _, err := m.Action(ids[2], "resume"); !errors.Is(err, ErrConflict) {
		t.Fatal("resumed cancelled task")
	}
	m.Close()
	for _, task := range m.List() {
		if task.State == "running" || task.State == "queued" {
			t.Fatal("shutdown lost state")
		}
	}
}

func TestIgnoredRangeSafelyRestarts(t *testing.T) {
	payload := bytes.Repeat([]byte("new-resource"), 32768)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("ETag", `"current"`)
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		// Deliberately ignore Range, as a source without resume support would.
		slowWriter{w}.Write(payload)
	}))
	defer server.Close()
	m := fixtureManager(t, t.TempDir(), server)
	task, err := m.Add("http://cdn.example/resource", "resource")
	if err != nil {
		t.Fatal(err)
	}
	await(t, m, task.ID, func(t Task) bool { return t.Bytes > 16384 })
	if _, err = m.Action(task.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Action(task.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	completed := await(t, m, task.ID, func(t Task) bool { return t.State == "completed" || t.State == "failed" })
	if completed.State != "completed" || requestCount.Load() < 2 {
		t.Fatalf("restart %+v", completed)
	}
	f, _, err := m.OpenCompleted(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, payload) {
		t.Fatal("Range ignored caused appended duplicate bytes")
	}
}

func TestDiskBudgetAndUnknownLength(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !unknown {
					w.Header().Set("Content-Length", "131072")
				} else {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				for range 32 {
					if _, err := w.Write(bytes.Repeat([]byte("x"), 4096)); err != nil {
						return
					}
					if unknown {
						w.(http.Flusher).Flush()
					}
				}
			}))
			defer server.Close()
			m := fixtureManager(t, t.TempDir(), server)
			var checks atomic.Int32
			m.space = func(string) (Storage, error) {
				available := ReserveBytes + uint64(65536)
				if unknown {
					available = ReserveBytes + 1024
					if checks.Add(1) < 2 {
						available = 1 << 40
					}
				}
				return Storage{AvailableBytes: available}, nil
			}
			task, err := m.Add("http://cdn.example/file", "")
			if err != nil {
				t.Fatal(err)
			}
			paused := await(t, m, task.ID, func(t Task) bool { return t.State == "paused" || t.State == "failed" || t.State == "completed" })
			if paused.State != "paused" || !strings.Contains(paused.Error, "disk space") {
				t.Fatalf("disk budget %+v", paused)
			}
			if !unknown && paused.Bytes != 0 {
				t.Fatal("known oversized download wrote data")
			}
		})
	}
}

func TestRejectUnsafeTargetsAndInvalidResume(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://127.0.0.1/private", 302)
			return
		}
		w.Header().Set("Content-Range", "bytes 99-199/200")
		w.WriteHeader(206)
		w.Write([]byte("incorrect"))
	}))
	defer server.Close()
	m := fixtureManager(t, t.TempDir(), server)
	for _, raw := range []string{"http://127.0.0.1/file", "http://192.168.1.1/file", "file:///etc/passwd", "https://user:pass@cdn.example/f"} {
		if _, err := m.Add(raw, ""); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := m.Add("http://cdn.example/file", "../outside"); err == nil {
		t.Fatal("accepted path escape")
	}
	for _, p := range []string{"/invalid-range", "/redirect"} {
		task, err := m.Add("http://cdn.example"+p, "")
		if err != nil {
			t.Fatal(err)
		}
		failed := await(t, m, task.ID, func(t Task) bool { return t.State == "failed" })
		if failed.Bytes != 0 {
			t.Fatal("unsafe response wrote data")
		}
	}
}

func TestStorageReportsRealFilesystem(t *testing.T) {
	dir := t.TempDir()
	s, err := diskSpace(dir)
	if err != nil || s.Path != dir || s.TotalBytes == 0 || s.AvailableBytes > s.FreeBytes || s.UsedBytes+s.FreeBytes != s.TotalBytes {
		t.Fatalf("invalid filesystem capacity %+v %v", s, err)
	}
}

func TestDirectoryLockAndInterruptedTaskRecovery(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := New(dir, nil); err == nil {
		other.Close()
		t.Fatal("two services can write the same download library")
	}
	m.Close()
	task := Task{ID: strings.Repeat("f", 32), Name: "file.bin", URL: "https://cdn.example/file", State: "running", Bytes: 123, TotalBytes: 1000}
	data, _ := json.Marshal([]Task{task})
	if err := os.WriteFile(dir+"/tasks.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	m, err = New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if tasks := m.List(); len(tasks) != 1 || tasks[0].State != "paused" || tasks[0].Bytes != 0 {
		t.Fatalf("interrupted task not reconciled: %+v", tasks)
	}
	for _, raw := range []string{"127.0.0.1", "100.100.100.100", "169.254.169.254", "192.168.1.1", "::1", "::ffff:127.0.0.1", "fc00::1"} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Fatalf("allowed non-public destination %s", raw)
		}
	}
}
