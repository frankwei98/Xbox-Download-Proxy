package offline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTaskMetadataWriteFailureRollsBackAddAndActions(t *testing.T) {
	for _, action := range []string{"add", "pause", "cancel", "resume"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			task := Task{ID: strings.Repeat("d", 32), Name: "file", URL: "http://cdn.example/file", State: "paused", TotalBytes: -1}
			writeLibrary(t, dir, []Task{task}, nil)
			m, err := New(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			// A path type conflict is deterministic even when tests run as root.
			metadata := filepath.Join(dir, "tasks.json")
			if err := os.Remove(metadata); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(metadata, 0700); err != nil {
				t.Fatal(err)
			}
			if action == "add" {
				_, err = m.Add("http://cdn.example/new", "new-file")
			} else {
				_, err = m.Action(task.ID, action)
			}
			if err == nil {
				t.Fatal("operation concealed a metadata write failure")
			}
			list := m.List()
			if len(list) != 1 || list[0].ID != task.ID || list[0].State != "paused" {
				t.Fatalf("failed %s changed tasks: %+v", action, list)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != ".lock" && entry.Name() != "tasks.json" {
					t.Fatalf("failed operation leaked a task directory or temporary file: %s", entry.Name())
				}
			}
		})
	}
}

func TestDownloadTaskLimitPreservesExistingLibrary(t *testing.T) {
	dir := t.TempDir()
	tasks := make([]Task, 1000)
	for i := range tasks {
		tasks[i] = Task{ID: fmt.Sprintf("%032x", i), Name: "file", URL: "http://cdn.example/file", State: "paused", TotalBytes: -1}
	}
	writeLibrary(t, dir, tasks, nil)
	m, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.Add("http://cdn.example/another", "file"); err == nil || !strings.Contains(err.Error(), "at most 1000") {
		t.Fatalf("task quota error = %v", err)
	}
	if len(m.List()) != 1000 {
		t.Fatal("quota rejection changed existing tasks")
	}
}

func TestDownloadStorageErrorsAreReportedBeforeWriting(t *testing.T) {
	for _, duringWrite := range []bool{false, true} {
		t.Run(fmt.Sprint(duringWrite), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "payload") }))
			t.Cleanup(server.Close)
			m := fixtureManager(t, t.TempDir(), server)
			checks := 0
			m.space = func(string) (Storage, error) {
				checks++
				if !duringWrite || checks > 1 {
					return Storage{}, errors.New("fixture storage unavailable")
				}
				return Storage{AvailableBytes: 1 << 40}, nil
			}
			task, err := m.Add("http://cdn.example/file", "")
			if err != nil {
				t.Fatal(err)
			}
			final := await(t, m, task.ID, func(task Task) bool { return task.State == "failed" || task.State == "completed" })
			if final.State != "failed" || final.Bytes != 0 || !strings.Contains(final.Error, "fixture storage unavailable") {
				t.Fatalf("storage failure was not preserved: %+v", final)
			}
		})
	}
}

func TestDownloadResolutionErrorDoesNotDial(t *testing.T) {
	var resolutions, dials atomic.Int32
	m, err := New(t.TempDir(), func(context.Context, string) ([]string, error) {
		resolutions.Add(1)
		return nil, errors.New("fixture DNS failure")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.dial = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("unexpected dial after DNS failure")
	}
	task, err := m.Add("http://cdn.example/file", "")
	if err != nil {
		t.Fatal(err)
	}
	final := await(t, m, task.ID, func(task Task) bool { return task.State == "failed" || task.State == "completed" })
	if final.State != "failed" || final.Bytes != 0 || !strings.Contains(final.Error, "no public CDN address") || resolutions.Load() != 1 || dials.Load() != 0 {
		t.Fatalf("DNS failure result = %+v, resolutions=%d, dials=%d", final, resolutions.Load(), dials.Load())
	}
}
