package offline

import (
	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"
	"xboxdownloadproxy/internal/domain"
)

// Only versioned Xbox content paths can omit expiring query signatures from
// their identity. Other paths must retain the complete query string.
var versionedXboxPath = regexp.MustCompile(`(?i)/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+\.[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(/|[._-]|$)`)

func ResourceKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "80") || !domain.LocalDeliveryHost(u.Hostname()) || u.Path == "" {
		return ""
	}
	key := u.EscapedPath()
	if !versionedXboxPath.MatchString(key) && (u.RawQuery != "" || u.ForceQuery) {
		key += "?" + u.RawQuery
	}
	return key
}

func sourceURL(task *Task) string {
	if task.OriginalURL != "" {
		return task.OriginalURL
	}
	return task.URL // Libraries created before local delivery retain their URL.
}

func (m *Manager) CanDeliver(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[id]
	if m.closed || task == nil || task.State != "completed" || ResourceKey(sourceURL(task)) == "" {
		return false
	}
	info, err := m.root.Stat(taskFile(task))
	return err == nil && info.Mode().IsRegular() && info.Size() == task.TotalBytes
}

// OpenResource never exposes partial files or resolves a request as a filesystem
// path. The newest valid completed task wins when the same URL was downloaded
// more than once. The open descriptor remains usable if its task is deleted.
func (m *Manager) OpenResource(raw string) (*os.File, Task, error) {
	key := ResourceKey(raw)
	if key == "" {
		return nil, Task{}, ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, Task{}, ErrNotFound
	}
	var chosen *Task
	var file *os.File
	for _, task := range m.tasks {
		if task.State != "completed" || ResourceKey(sourceURL(task)) != key || (chosen != nil && (task.CreatedAt.Before(chosen.CreatedAt) || (task.CreatedAt.Equal(chosen.CreatedAt) && task.ID <= chosen.ID))) {
			continue
		}
		f, _, err := m.openCompletedLocked(task.ID)
		if err != nil {
			continue
		}
		if file != nil {
			file.Close()
		}
		chosen, file = task, f
	}
	if file == nil {
		return nil, Task{}, ErrNotFound
	}
	return file, *chosen, nil
}

// A local file's modification time is not an origin Last-Modified validator.
// Callers should use the recorded origin value when available.
func ResourceModTime(task Task) time.Time {
	if t, err := http.ParseTime(task.LastModified); err == nil {
		return t
	}
	return time.Time{}
}
