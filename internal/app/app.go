package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"
	"xboxdownloadproxy/internal/candidates"
	"xboxdownloadproxy/internal/config"
	"xboxdownloadproxy/internal/domain"
	"xboxdownloadproxy/internal/engine"
	"xboxdownloadproxy/internal/offline"
	"xboxdownloadproxy/internal/speedtest"
)

var ErrRunning = errors.New("stop the download service before changing configuration")

type Event struct {
	ID      uint64    `json:"id"`
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Client  string    `json:"client"`
	Host    string    `json:"host"`
	Message string    `json:"message"`
	URL     string    `json:"url,omitempty"`
}
type App struct {
	mu          sync.Mutex
	dir         string
	cfg         config.Config
	webListen   string
	token       string
	runtime     *engine.Runtime
	started     time.Time
	lastError   string
	metrics     engine.Metrics
	candidates  *candidates.Store
	tests       *speedtest.Manager
	downloads   *offline.Manager
	downloadErr error
	eventMu     sync.Mutex
	events      []Event
	nextEvent   uint64
}
type Addresses struct {
	DNS   string `json:"dns"`
	HTTP  string `json:"http"`
	TLS   string `json:"tls"`
	Web   string `json:"web"`
	LANIP string `json:"lanIP"`
}
type Status struct {
	Running   bool           `json:"running"`
	StartTime *time.Time     `json:"startTime,omitempty"`
	LastError string         `json:"lastError,omitempty"`
	Addresses Addresses      `json:"addresses"`
	Metrics   engine.Metrics `json:"metrics"`
}

func New(dir, token string, c config.Config) *App {
	return NewWithDownloads(dir, token, c, "")
}
func NewWithDownloads(dir, token string, c config.Config, downloadDir string) *App {
	a := &App{dir: dir, token: token, cfg: config.Clone(c), webListen: c.WebListen, candidates: candidates.New(dir), tests: speedtest.New(), events: []Event{}}
	if downloadDir == "" {
		downloadDir = filepath.Join(dir, "downloads")
	}
	a.downloads, a.downloadErr = offline.New(downloadDir, func(ctx context.Context, host string) ([]string, error) {
		cfg := a.Config()
		if group := domain.GroupFor(host); group != "" && cfg.Groups[group].IP != "" {
			return []string{cfg.Groups[group].IP}, nil
		}
		return engine.ResolveIPv4(ctx, host, cfg.UpstreamDNS)
	})
	return a
}
func (a *App) DownloadError() error  { return a.downloadErr }
func (a *App) Config() config.Config { a.mu.Lock(); defer a.mu.Unlock(); return config.Clone(a.cfg) }
func (a *App) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := Status{Running: a.runtime != nil, LastError: a.lastError, Addresses: Addresses{a.cfg.DNSListen, a.cfg.HTTPListen, a.cfg.TLSListen, a.webListen, a.cfg.LANIP}, Metrics: a.metrics}
	if a.runtime != nil {
		s.Metrics = a.runtime.Metrics()
		start := a.started
		s.StartTime = &start
	}
	return s
}
func (a *App) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runtime != nil {
		return nil
	}
	if err := config.Validate(a.cfg); err != nil {
		return err
	}
	r, e := engine.Start(a.cfg, a.log)
	if e != nil {
		a.lastError = e.Error()
		a.log("ERROR", "", "", e.Error(), "")
		return e
	}
	a.runtime = r
	a.started = time.Now().UTC()
	a.lastError = ""
	a.log("SERVICE", "", "", "下载服务已启动", "")
	return nil
}
func (a *App) stopLocked() {
	if a.runtime != nil {
		a.metrics = a.runtime.Metrics()
		a.runtime.Close()
		a.runtime = nil
		a.log("SERVICE", "", "", "下载服务已停止", "")
	}
}
func (a *App) Stop() { a.mu.Lock(); defer a.mu.Unlock(); a.stopLocked() }
func (a *App) Close() {
	a.tests.Close()
	if a.downloads != nil {
		a.downloads.Close()
	}
	a.Stop()
}
func (a *App) Update(c config.Config) error {
	if e := config.Validate(c); e != nil {
		return e
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runtime != nil {
		return ErrRunning
	}
	if e := config.Save(a.dir, c); e != nil {
		return e
	}
	a.cfg = config.Clone(c)
	a.log("CONFIG", "", "", "配置已保存", "")
	return nil
}
func (a *App) Apply(group, ip string) (config.Config, error) {
	if !config.PublicIPv4(ip) {
		return config.Config{}, errors.New("expected a public CDN IPv4 address")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	g, ok := a.cfg.Groups[group]
	if !ok {
		return config.Config{}, errors.New("unknown group")
	}
	next := config.Clone(a.cfg)
	g.IP = ip
	next.Groups[group] = g
	// Persist first: a failed write must not silently change or stop the live service.
	if e := config.Save(a.dir, next); e != nil {
		return config.Config{}, e
	}
	a.stopLocked()
	a.cfg = next
	a.log("CONFIG", "", group, "已应用 "+ip+"；请启动下载服务", "")
	return config.Clone(a.cfg), nil
}
func (a *App) log(kind, client, host, message, url string) {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	a.nextEvent++
	a.events = append(a.events, Event{a.nextEvent, time.Now().UTC(), kind, client, host, message, url})
	if len(a.events) > 500 {
		a.events = append([]Event(nil), a.events[len(a.events)-500:]...)
	}
}
func (a *App) Logs(after uint64) ([]Event, uint64) {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	out := []Event{}
	for _, e := range a.events {
		if e.ID > after {
			out = append(out, e)
		}
	}
	return out, a.nextEvent
}
func (a *App) Groups() []domain.Group {
	c := a.Config()
	out := append([]domain.Group{}, domain.Groups...)
	for i := range out {
		out[i].SourceURL = c.Groups[out[i].ID].SourceURL
		out[i].TestURL = c.Groups[out[i].ID].TestURL
	}
	return out
}
func (a *App) Candidates(ctx context.Context, group string, refresh bool) (candidates.List, error) {
	return a.candidates.Load(ctx, a.Config(), group, refresh)
}
func (a *App) Test(group, target string, ips []string) (speedtest.Job, error) {
	c := a.Config()
	g, ok := c.Groups[group]
	if !ok {
		return speedtest.Job{}, errors.New("unknown group")
	}
	if target == "" {
		target = g.TestURL
	}
	if target == "" {
		return speedtest.Job{}, fmt.Errorf("%s needs a real test download URL; signed Store URLs may expire", group)
	}
	return a.tests.Start(group, target, ips, c.Test)
}
