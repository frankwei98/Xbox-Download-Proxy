package offline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLibrary(t *testing.T, dir string, tasks []Task, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(tasks)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func taskByID(t *testing.T, m *Manager, id string) Task {
	t.Helper()
	for _, task := range m.List() {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("task %s is missing", id)
	return Task{}
}

func TestQueueStartsNextTaskWheneverAnActiveSlotIsReleased(t *testing.T) {
	for _, action := range []string{"complete", "fail", "pause", "cancel"} {
		t.Run(action, func(t *testing.T) {
			release := make(chan struct{})
			started := make(chan string, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started <- r.URL.Path
				switch r.URL.Path {
				case "/first":
					select {
					case <-release:
						if action == "fail" {
							w.WriteHeader(http.StatusInternalServerError)
						}
						_, _ = io.WriteString(w, "first-file")
					case <-r.Context().Done():
					}
				case "/second":
					<-r.Context().Done()
				case "/third":
					_, _ = io.WriteString(w, "queued-file")
				}
			}))
			t.Cleanup(server.Close)
			m := fixtureManager(t, t.TempDir(), server)
			var tasks []Task
			for _, path := range []string{"first", "second", "third"} {
				task, err := m.Add("http://cdn.example/"+path, path)
				if err != nil {
					t.Fatal(err)
				}
				tasks = append(tasks, task)
			}
			for range 2 {
				select {
				case path := <-started:
					if path == "/third" {
						t.Fatal("third download exceeded the two-task concurrency limit")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("initial downloads did not start")
				}
			}
			if got := taskByID(t, m, tasks[2].ID); got.State != "queued" {
				t.Fatalf("third task = %+v, want queued", got)
			}
			if action == "complete" || action == "fail" {
				close(release)
			} else if _, err := m.Action(tasks[0].ID, action); err != nil {
				t.Fatal(err)
			}
			finished := await(t, m, tasks[2].ID, func(task Task) bool { return task.State == "completed" || task.State == "failed" })
			if finished.State != "completed" {
				t.Fatalf("queue did not continue: %+v", finished)
			}
			f, _, err := m.OpenCompleted(finished.ID)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil || string(data) != "queued-file" {
				t.Fatalf("queued download = %q, %v", data, err)
			}
			if got := taskByID(t, m, tasks[1].ID); got.State != "running" {
				t.Fatalf("unrelated active task changed: %+v", got)
			}
		})
	}
}

func TestResumeValidatesSourceAndNeverSplicesDifferentResources(t *testing.T) {
	const modified = "Mon, 19 Oct 2026 00:00:00 GMT"
	for _, tc := range []struct {
		name, etag, modified, responseETag, responseModified string
		contentRange, body, wantRange, wantValidator         string
		wantError                                            string
		status                                               int
	}{
		{name: "matching ETag", etag: `"v1"`, responseETag: `"v1"`, contentRange: "bytes 4-9/10", body: "efghij", wantRange: "bytes=4-", wantValidator: `"v1"`, status: 206},
		{name: "changed ETag", etag: `"v1"`, responseETag: `"v2"`, contentRange: "bytes 4-9/10", body: "XXXXXX", wantRange: "bytes=4-", wantValidator: `"v1"`, status: 206, wantError: "resource changed"},
		{name: "matching Last-Modified", modified: modified, responseModified: modified, contentRange: "bytes 4-9/10", body: "efghij", wantRange: "bytes=4-", wantValidator: modified, status: 206},
		{name: "changed Last-Modified", modified: modified, responseModified: "Tue, 20 Oct 2026 00:00:00 GMT", contentRange: "bytes 4-9/10", body: "XXXXXX", wantRange: "bytes=4-", wantValidator: modified, status: 206, wantError: "resource changed"},
		{name: "partial response may omit validator", etag: `"v1"`, contentRange: "bytes 4-9/10", body: "efghij", wantRange: "bytes=4-", wantValidator: `"v1"`, status: 206},
		{name: "no validator restarts", body: "abcdefghij", status: 200},
		{name: "weak ETag alone restarts", etag: `W/"v1"`, body: "abcdefghij", status: 200},
		{name: "weak ETag falls back to Last-Modified", etag: `W/"v1"`, modified: modified, responseETag: `W/"v1"`, responseModified: modified, contentRange: "bytes 4-9/10", body: "efghij", wantRange: "bytes=4-", wantValidator: modified, status: 206},
		{name: "If-Range mismatch replaces partial file", etag: `"v1"`, responseETag: `"v2"`, body: "abcdefghij", wantRange: "bytes=4-", wantValidator: `"v1"`, status: 200},
		{name: "wrong offset rejected", etag: `"v1"`, contentRange: "bytes 3-8/10", body: "defghi", wantRange: "bytes=4-", wantValidator: `"v1"`, status: 206, wantError: "invalid Content-Range"},
		{name: "range length mismatch rejected", etag: `"v1"`, contentRange: "bytes 4-8/10", body: "efghij", wantRange: "bytes=4-", wantValidator: `"v1"`, status: 206, wantError: "invalid Content-Range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != tc.wantRange || r.Header.Get("If-Range") != tc.wantValidator || r.Header.Get("Accept-Encoding") != "identity" {
					t.Errorf("request headers = %v", r.Header)
				}
				w.Header().Set("ETag", tc.responseETag)
				w.Header().Set("Last-Modified", tc.responseModified)
				w.Header().Set("Content-Range", tc.contentRange)
				w.Header().Set("Content-Length", fmt.Sprint(len(tc.body)))
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			dir := t.TempDir()
			task := Task{ID: strings.Repeat("a", 32), Name: "file.bin", URL: "http://cdn.example/file", State: "paused", Bytes: 4, TotalBytes: 10, ETag: tc.etag, LastModified: tc.modified}
			writeLibrary(t, dir, []Task{task}, map[string][]byte{taskFile(&task): []byte("abcd")})
			m := fixtureManager(t, dir, server)
			if _, err := m.Action(task.ID, "resume"); err != nil {
				t.Fatal(err)
			}
			final := await(t, m, task.ID, func(task Task) bool { return task.State == "completed" || task.State == "failed" })
			data, err := os.ReadFile(filepath.Join(dir, taskFile(&task)))
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" {
				if final.State != "failed" || !strings.Contains(final.Error, tc.wantError) || string(data) != "abcd" {
					t.Fatalf("unsafe resume = %+v, file=%q", final, data)
				}
			} else if final.State != "completed" || final.Bytes != 10 || final.TotalBytes != 10 || string(data) != "abcdefghij" {
				t.Fatalf("resume = %+v, file=%q", final, data)
			}
			if tc.name == "partial response may omit validator" && final.ETag != `"v1"` {
				t.Fatal("resume lost the validator needed for the next interruption")
			}
		})
	}
}

func TestUnsatisfiableResumeRetriesWithoutRange(t *testing.T) {
	requests := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Header.Get("Range")
		if r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		_, _ = io.WriteString(w, "new-file")
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	task := Task{ID: strings.Repeat("b", 32), Name: "file", URL: "http://cdn.example/file", State: "paused", ETag: `"old"`, TotalBytes: 100}
	writeLibrary(t, dir, []Task{task}, map[string][]byte{taskFile(&task): []byte("old-partial")})
	m := fixtureManager(t, dir, server)
	if _, err := m.Action(task.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	final := await(t, m, task.ID, func(task Task) bool { return task.State == "completed" || task.State == "failed" })
	if final.State != "completed" || final.Bytes != 8 || len(requests) != 2 || <-requests != "bytes=11-" || <-requests != "" {
		t.Fatalf("416 fallback failed: %+v", final)
	}
	data, err := os.ReadFile(filepath.Join(dir, taskFile(&task)))
	if err != nil || string(data) != "new-file" {
		t.Fatalf("restart body = %q, %v", data, err)
	}
}

func TestDownloadFailuresDoNotExposeCompletedFiles(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		handler    http.HandlerFunc
	}{
		{"expired signed URL", "HTTP 403", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }},
		{"compressed response", "compressed", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write([]byte("compressed"))
		}},
		{"truncated body", "unexpected EOF", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, "short")
		}},
		{"redirect loop", "too many download redirects", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://cdn.example/loop", 302) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			t.Cleanup(server.Close)
			m := fixtureManager(t, t.TempDir(), server)
			task, err := m.Add("http://cdn.example/file", "")
			if err != nil {
				t.Fatal(err)
			}
			final := await(t, m, task.ID, func(task Task) bool { return task.State == "failed" || task.State == "completed" })
			if final.State != "failed" || !strings.Contains(final.Error, tc.want) {
				t.Fatalf("download = %+v, want %q", final, tc.want)
			}
			if f, _, err := m.OpenCompleted(task.ID); !errors.Is(err, ErrConflict) {
				if f != nil {
					f.Close()
				}
				t.Fatalf("failed file is available: %v", err)
			}
		})
	}
}

func TestUnknownLengthDownloadCompletesWithExactByteCount(t *testing.T) {
	payload := bytes.Repeat([]byte("unknown-length"), 4096)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	m := fixtureManager(t, t.TempDir(), server)
	task, err := m.Add("http://cdn.example/file", "")
	if err != nil {
		t.Fatal(err)
	}
	final := await(t, m, task.ID, func(task Task) bool { return task.State == "completed" || task.State == "failed" })
	if final.State != "completed" || final.Bytes != int64(len(payload)) || final.TotalBytes != int64(len(payload)) {
		t.Fatalf("unknown-length task = %+v", final)
	}
	f, _, err := m.OpenCompleted(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatal("unknown-length payload differs", err)
	}
}
