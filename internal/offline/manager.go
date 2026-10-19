package offline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"xboxdownloadproxy/internal/config"
)

var (
	ErrNotFound = errors.New("download task not found")
	ErrConflict = errors.New("download task cannot perform this action in its current state")
	ErrSpace    = errors.New("insufficient disk space; keep at least 256 MiB free, then resume the task")
)

type Task struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	Name         string    `json:"name"`
	State        string    `json:"state"`
	Bytes        int64     `json:"bytes"`
	TotalBytes   int64     `json:"totalBytes"`
	SpeedBPS     float64   `json:"speedBPS"`
	Error        string    `json:"error,omitempty"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"lastModified,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Resolve func(context.Context, string) ([]string, error)
type control struct {
	cancel  context.CancelFunc
	done    chan struct{}
	desired string
}
type Manager struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	path    string
	root    *os.Root
	lock    *os.File
	tasks   map[string]*Task
	active  map[string]*control
	closed  bool
	wg      sync.WaitGroup
	resolve Resolve
	dial    func(context.Context, string, string) (net.Conn, error)
	space   func(string) (Storage, error)
}

func New(dir string, resolve Resolve) (*Manager, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	m := &Manager{path: abs, root: r, tasks: map[string]*Task{}, active: map[string]*control{}, resolve: resolve, space: diskSpace, dial: (&net.Dialer{Timeout: 10 * time.Second}).DialContext}
	m.lock, err = r.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		r.Close()
		return nil, err
	}
	if err = syscall.Flock(int(m.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		m.lock.Close()
		r.Close()
		return nil, errors.New("download directory is already used by another service")
	}
	loaded := false
	defer func() {
		if !loaded {
			m.lock.Close()
		}
	}()
	f, err := r.Open("tasks.json")
	if err == nil {
		defer f.Close()
		var tasks []Task
		err = json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&tasks)
		if err != nil {
			r.Close()
			return nil, fmt.Errorf("read download tasks: %w", err)
		}
		for _, task := range tasks {
			if !validID(task.ID) || !validName(task.Name) || validateURL(task.URL) != nil || m.tasks[task.ID] != nil || task.Bytes < 0 || task.TotalBytes < -1 || !validState(task.State) {
				r.Close()
				return nil, errors.New("invalid persisted download task")
			}
			task.SpeedBPS = 0
			if task.State == "running" || task.State == "queued" {
				task.State = "paused"
			}
			info, statErr := r.Stat(taskFile(&task))
			if statErr == nil && info.Mode().IsRegular() {
				task.Bytes = info.Size()
				if task.State == "completed" && (task.TotalBytes < 0 || task.Bytes != task.TotalBytes) {
					task.State, task.Error = "failed", "downloaded file size changed; resume to download again"
					task.ETag, task.LastModified = "", ""
				}
			} else if task.State == "completed" {
				task.State, task.Error = "failed", "downloaded file is missing"
				task.Bytes = 0
			} else if os.IsNotExist(statErr) {
				task.Bytes = 0
			} else if statErr != nil {
				r.Close()
				return nil, fmt.Errorf("read download file: %w", statErr)
			}
			m.tasks[task.ID] = &task
		}
	} else if !os.IsNotExist(err) {
		r.Close()
		return nil, err
	}
	if err := m.saveLocked(); err != nil {
		r.Close()
		return nil, err
	}
	loaded = true
	return m, nil
}

func validState(s string) bool {
	switch s {
	case "queued", "running", "paused", "completed", "failed", "cancelled":
		return true
	}
	return false
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && name != "tasks.json" && len(name) <= 180 && !strings.ContainsAny(name, "/\\") && strings.IndexFunc(name, unicode.IsControl) < 0
}
func validateURL(raw string) error {
	if len(raw) > 16384 {
		return errors.New("download URL is too long")
	}
	return config.ValidateTestURL(raw)
}
func taskFile(t *Task) string { return t.ID + "/" + t.Name }
func (m *Manager) List() []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked()
}
func (m *Manager) listLocked() []Task {
	out := make([]Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		out = append(out, *task)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
func (m *Manager) saveLocked() error {
	b, err := json.MarshalIndent(m.listLocked(), "", "  ")
	if err != nil {
		return err
	}
	return config.AtomicWrite(filepath.Join(m.path, "tasks.json"), b)
}

func (m *Manager) Add(raw, name string) (Task, error) {
	if err := validateURL(raw); err != nil {
		return Task{}, err
	}
	u, _ := url.Parse(raw)
	if literal, err := netip.ParseAddr(u.Hostname()); err == nil && !publicIP(literal) {
		return Task{}, errors.New("downloads require a public internet address")
	}
	if name == "" {
		name = path.Base(u.Path)
		if !validName(name) {
			name = "download.bin"
		}
	}
	if !validName(name) {
		return Task{}, errors.New("filename must be a single name of at most 180 bytes without control characters")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	task := Task{ID: hex.EncodeToString(b), URL: raw, Name: name, State: "queued", TotalBytes: -1, CreatedAt: now, UpdatedAt: now}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Task{}, errors.New("download manager is closed")
	}
	if len(m.tasks) >= 1000 {
		return Task{}, errors.New("at most 1000 tasks; remove old tasks before adding more")
	}
	if err := m.root.Mkdir(task.ID, 0700); err != nil {
		return Task{}, err
	}
	m.tasks[task.ID] = &task
	if err := m.saveLocked(); err != nil {
		delete(m.tasks, task.ID)
		_ = m.root.Remove(task.ID)
		return Task{}, err
	}
	out := task
	m.scheduleLocked()
	return out, nil
}

func (m *Manager) scheduleLocked() {
	if m.closed {
		return
	}
	for _, snapshot := range m.listLocked() {
		if len(m.active) >= 2 {
			return
		}
		task := m.tasks[snapshot.ID]
		if task.State != "queued" {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		ctrl := &control{cancel: cancel, done: make(chan struct{})}
		task.State, task.Error, task.UpdatedAt = "running", "", time.Now().UTC()
		m.active[task.ID] = ctrl
		m.wg.Add(1)
		go m.run(ctx, task.ID, ctrl)
	}
}

func (m *Manager) Action(id, action string) (Task, error) {
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return Task{}, ErrNotFound
	}
	if m.closed {
		m.mu.Unlock()
		return Task{}, ErrConflict
	}
	if action == "resume" {
		if task.State != "paused" && task.State != "failed" {
			m.mu.Unlock()
			return Task{}, ErrConflict
		}
		prev := *task
		task.State, task.Error, task.UpdatedAt = "queued", "", time.Now().UTC()
		if err := m.saveLocked(); err != nil {
			*task = prev
			m.mu.Unlock()
			return Task{}, err
		}
		m.scheduleLocked()
		out := *task
		m.mu.Unlock()
		return out, nil
	}
	if action != "pause" && action != "cancel" {
		m.mu.Unlock()
		return Task{}, errors.New("unknown task action")
	}
	if task.State == "completed" || task.State == "cancelled" {
		m.mu.Unlock()
		return Task{}, ErrConflict
	}
	state := "paused"
	if action == "cancel" {
		state = "cancelled"
	}
	if ctrl := m.active[id]; ctrl != nil {
		ctrl.desired = state
		ctrl.cancel()
		done := ctrl.done
		m.mu.Unlock()
		<-done
		m.mu.Lock()
		task, ok = m.tasks[id]
		if !ok {
			m.mu.Unlock()
			return Task{}, ErrNotFound
		}
	} else {
		prev := *task
		task.State, task.SpeedBPS, task.UpdatedAt = state, 0, time.Now().UTC()
		if err := m.saveLocked(); err != nil {
			*task = prev
			m.mu.Unlock()
			return Task{}, err
		}
	}
	out := *task
	m.mu.Unlock()
	return out, nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[id]
	if !ok {
		return ErrNotFound
	}
	if m.active[id] != nil {
		return ErrConflict
	}
	if err := m.root.Remove(taskFile(task)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := m.root.Remove(id); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(m.tasks, id)
	if err := m.saveLocked(); err != nil {
		m.tasks[id] = task
		return err
	}
	return nil
}

func (m *Manager) OpenCompleted(id string) (*os.File, Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[id]
	if !ok {
		return nil, Task{}, ErrNotFound
	}
	if task.State != "completed" {
		return nil, Task{}, ErrConflict
	}
	f, err := m.root.Open(taskFile(task))
	if err != nil {
		return nil, Task{}, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != task.TotalBytes {
		f.Close()
		return nil, Task{}, errors.New("download file is missing or has changed")
	}
	return f, *task, nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, task := range m.tasks {
		if task.State == "queued" {
			task.State = "paused"
		}
	}
	for _, ctrl := range m.active {
		ctrl.desired = "paused"
		ctrl.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.saveLocked()
	_ = m.root.Close()
	_ = m.lock.Close()
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublic {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"),
}

type idleConn struct{ net.Conn }

func (c idleConn) Read(p []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	return c.Conn.Read(p)
}

func (m *Manager) transport() *http.Transport {
	return &http.Transport{
		DisableCompression: true, ResponseHeaderTimeout: 15 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var ips []string
			if ip, err := netip.ParseAddr(host); err == nil {
				ips = []string{ip.String()}
			} else if m.resolve != nil {
				ips, err = m.resolve(ctx, host)
			} else {
				ips, err = net.DefaultResolver.LookupHost(ctx, host)
			}
			if err != nil {
				return nil, err
			}
			last := errors.New("no public CDN address found")
			for _, raw := range ips {
				ip, err := netip.ParseAddr(raw)
				if err != nil || !publicIP(ip) {
					continue
				}
				conn, err := m.dial(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return idleConn{conn}, nil
				}
				last = err
			}
			return nil, last
		},
	}
}
