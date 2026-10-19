package offline

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func persistenceTask(id, name, state string) Task {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	return Task{ID: id, URL: "https://cdn.example.test/file.bin", Name: name, State: state, TotalBytes: -1, CreatedAt: now, UpdatedAt: now}
}

func persistenceWriteTasks(t *testing.T, dir string, tasks []Task) {
	t.Helper()
	b, err := json.Marshal(tasks)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func persistenceWriteFile(t *testing.T, dir string, task Task, data []byte) {
	t.Helper()
	taskDir := filepath.Join(dir, task.ID)
	if err := os.MkdirAll(taskDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, task.Name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPersistenceRejectsCorruptAndInvalidMetadataAndReleasesLock(t *testing.T) {
	t.Run("corrupt JSON", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		if m, err := New(dir, nil); err == nil {
			m.Close()
			t.Fatal("New accepted corrupt JSON")
		}
		if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte("[]"), 0600); err != nil {
			t.Fatal(err)
		}
		m, err := New(dir, nil)
		if err != nil {
			t.Fatalf("lock remained held after corrupt JSON: %v", err)
		}
		m.Close()
	})

	base := persistenceTask(strings.Repeat("a", 32), "valid.bin", "paused")
	invalid := []struct {
		name  string
		tasks []Task
	}{
		{"ID", []Task{func() Task { x := base; x.ID = "bad"; return x }()}},
		{"name", []Task{func() Task { x := base; x.Name = "../outside"; return x }()}},
		{"URL", []Task{func() Task { x := base; x.URL = "file:///etc/passwd"; return x }()}},
		{"OriginalURL", []Task{func() Task { x := base; x.OriginalURL = "https://u:p@cdn.example.test/file"; return x }()}},
		{"duplicate ID", []Task{base, base}},
		{"negative bytes", []Task{func() Task { x := base; x.Bytes = -1; return x }()}},
		{"invalid total", []Task{func() Task { x := base; x.TotalBytes = -2; return x }()}},
		{"invalid state", []Task{func() Task { x := base; x.State = "done"; return x }()}},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			persistenceWriteTasks(t, dir, tt.tasks)
			if m, err := New(dir, nil); err == nil {
				m.Close()
				t.Fatal("New accepted invalid persisted task")
			}
			persistenceWriteTasks(t, dir, nil)
			m, err := New(dir, nil)
			if err != nil {
				t.Fatalf("lock remained held after rejecting metadata: %v", err)
			}
			m.Close()
		})
	}
}

func TestPersistenceReconcilesInterruptedAndCompletedFiles(t *testing.T) {
	dir := t.TempDir()
	partial := persistenceTask(strings.Repeat("1", 32), "partial.bin", "running")
	partial.Bytes, partial.TotalBytes, partial.SpeedBPS = 99, 100, 12
	queued := persistenceTask(strings.Repeat("2", 32), "queued.bin", "queued")
	missing := persistenceTask(strings.Repeat("3", 32), "missing.bin", "completed")
	missing.TotalBytes = 3
	truncated := persistenceTask(strings.Repeat("4", 32), "truncated.bin", "completed")
	truncated.Bytes, truncated.TotalBytes, truncated.ETag, truncated.LastModified = 9, 9, `"old"`, "Mon, 02 Jan 2006 15:04:05 GMT"
	persistenceWriteFile(t, dir, partial, []byte("partial payload"))
	persistenceWriteFile(t, dir, truncated, []byte("cut"))
	persistenceWriteTasks(t, dir, []Task{partial, queued, missing, truncated})

	m, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	byID := make(map[string]Task)
	for _, task := range m.List() {
		byID[task.ID] = task
	}
	if got := byID[partial.ID]; got.State != "paused" || got.Bytes != int64(len("partial payload")) || got.SpeedBPS != 0 {
		t.Errorf("running task recovery = %+v", got)
	}
	if got := byID[queued.ID]; got.State != "paused" || got.Bytes != 0 {
		t.Errorf("queued task recovery = %+v", got)
	}
	if got := byID[missing.ID]; got.State != "failed" || got.Bytes != 0 || !strings.Contains(got.Error, "missing") {
		t.Errorf("missing completed file recovery = %+v", got)
	}
	if got := byID[truncated.ID]; got.State != "failed" || got.Bytes != 3 || got.ETag != "" || got.LastModified != "" {
		t.Errorf("truncated completed file recovery = %+v", got)
	}
}

func TestPersistenceRejectsInvalidAddActionAndClosedManager(t *testing.T) {
	dir := t.TempDir()
	valid := persistenceTask(strings.Repeat("5", 32), "paused.bin", "paused")
	persistenceWriteFile(t, dir, valid, []byte("x"))
	persistenceWriteTasks(t, dir, []Task{valid})
	m, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, tc := range []struct{ raw, name string }{
		{"file:///etc/passwd", "valid.bin"},
		{"https://user:secret@cdn.example.test/file.bin", "valid.bin"},
		{"https://cdn.example.test/file.bin", "../escape"},
		{"https://cdn.example.test/file.bin", "bad\nname"},
	} {
		if _, err := m.Add(tc.raw, tc.name); err == nil {
			t.Errorf("Add(%q, %q) succeeded", tc.raw, tc.name)
		}
	}
	if _, err := m.Action(strings.Repeat("f", 32), "pause"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown ID Action error = %v", err)
	}
	if _, err := m.Action(valid.ID, "explode"); err == nil || err.Error() != "unknown task action" {
		t.Fatalf("unknown action error = %v", err)
	}
	m.Close()
	if _, err := m.Add("https://cdn.example.test/new.bin", "new.bin"); err == nil {
		t.Fatal("Add succeeded on closed manager")
	}
	if _, err := m.Action(valid.ID, "pause"); !errors.Is(err, ErrConflict) {
		t.Fatalf("closed manager Action error = %v", err)
	}
}

func TestListSnapshotsDoNotExposeStoredTasks(t *testing.T) {
	task := persistenceTask(strings.Repeat("6", 32), "snapshot.bin", "paused")
	dir := t.TempDir()
	persistenceWriteTasks(t, dir, []Task{task})
	m, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	snapshot := m.List()
	snapshot[0].Name = "changed.bin"
	snapshot[0].State = "completed"
	snapshot[0].URL = "https://attacker.invalid/"
	got := m.List()[0]
	if got.Name != task.Name || got.State != task.State || got.URL != task.URL {
		t.Fatalf("List snapshot mutation changed manager state: %+v", got)
	}
}

func TestCompletedResourcesPreferNewestValidAndOpenFDSurvivesDelete(t *testing.T) {
	dir := t.TempDir()
	resource := "http://assets1.xboxlive.com/content/game.bin?sig=one"
	older := persistenceTask(strings.Repeat("a", 32), "older.bin", "completed")
	older.OriginalURL, older.TotalBytes, older.CreatedAt = resource, 5, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	newest := persistenceTask(strings.Repeat("b", 32), "newest.bin", "completed")
	newest.OriginalURL, newest.TotalBytes, newest.CreatedAt = resource, 6, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	newerButInvalid := persistenceTask(strings.Repeat("c", 32), "truncated.bin", "completed")
	newerButInvalid.OriginalURL, newerButInvalid.TotalBytes, newerButInvalid.CreatedAt = resource, 20, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	persistenceWriteFile(t, dir, older, []byte("older"))
	persistenceWriteFile(t, dir, newest, []byte("newest"))
	persistenceWriteFile(t, dir, newerButInvalid, []byte("cut"))
	persistenceWriteTasks(t, dir, []Task{older, newest, newerButInvalid})

	m, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if !m.CanDeliver(older.ID) || !m.CanDeliver(newest.ID) || m.CanDeliver(newerButInvalid.ID) {
		t.Fatal("CanDeliver did not verify completed file lengths")
	}
	storage, err := m.Storage()
	if err != nil || storage.DownloadBytes != uint64(len("oldernewestcut")) {
		t.Fatalf("Storage() = %+v, %v; want all regular task files accounted", storage, err)
	}
	f, picked, err := m.OpenResource("http://ASSETS1.XBOXLIVE.COM/content/game.bin?sig=one")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if picked.ID != newest.ID {
		t.Fatalf("OpenResource selected %s, want newest valid %s", picked.ID, newest.ID)
	}
	if err := m.Delete(newest.ID); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(data, []byte("newest")) {
		t.Fatalf("opened file after delete = %q, %v", data, err)
	}
}

func TestResourceModTimeParsingAndInvalidAccess(t *testing.T) {
	want := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	valid := Task{LastModified: want.Format(http.TimeFormat)}
	if got := ResourceModTime(valid); !got.Equal(want) {
		t.Fatalf("ResourceModTime(valid) = %v, want %v", got, want)
	}
	for _, task := range []Task{{LastModified: "yesterday"}, {}} {
		if got := ResourceModTime(task); !got.IsZero() {
			t.Errorf("ResourceModTime(%q) = %v, want zero", task.LastModified, got)
		}
	}
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, _, err := m.OpenCompleted(strings.Repeat("e", 32)); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown OpenCompleted error = %v", err)
	}
	if _, _, err := m.OpenResource("https://assets1.xboxlive.com/resource"); !errors.Is(err, ErrNotFound) {
		t.Errorf("invalid resource URL error = %v", err)
	}
	if _, _, err := m.OpenResource("http://assets1.xboxlive.com/resource"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing resource error = %v", err)
	}
}
