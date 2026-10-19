package speedtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
	"xboxdownloadproxy/internal/config"
)

type Result struct {
	IP        string  `json:"ip"`
	LatencyMS float64 `json:"latencyMS"`
	Bytes     int64   `json:"bytes"`
	Seconds   float64 `json:"seconds"`
	Mbps      float64 `json:"mbps"`
	Error     string  `json:"error,omitempty"`
}
type Job struct {
	ID        string   `json:"id"`
	Group     string   `json:"group"`
	TestURL   string   `json:"testURL"`
	State     string   `json:"state"`
	Results   []Result `json:"results"`
	Completed int      `json:"completed"`
	Total     int      `json:"total"`
}
type Manager struct {
	mu     sync.Mutex
	job    Job
	cancel context.CancelFunc
	probe  func(context.Context, string, string, config.Test) Result
}

func New() *Manager { return &Manager{probe: Measure} }
func (m *Manager) Start(group, target string, ips []string, limits config.Test) (Job, error) {
	if err := config.ValidateTestURL(target); err != nil {
		return Job{}, err
	}
	if len(ips) == 0 || len(ips) > 128 {
		return Job{}, errors.New("select between 1 and 128 candidates")
	}
	seen := map[string]bool{}
	unique := []string{}
	for _, ip := range ips {
		if !config.PublicIPv4(ip) {
			return Job{}, fmt.Errorf("invalid public IPv4: %s", ip)
		}
		if !seen[ip] {
			seen[ip] = true
			unique = append(unique, ip)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job.State == "running" {
		return Job{}, errors.New("a speed test is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	var id [8]byte
	_, _ = rand.Read(id[:])
	m.job = Job{ID: hex.EncodeToString(id[:]), Group: group, TestURL: target, State: "running", Results: []Result{}, Total: len(unique)}
	job := m.snapshot()
	go m.run(ctx, job.ID, target, unique, limits)
	return job, nil
}
func (m *Manager) snapshot() Job {
	j := m.job
	j.Results = append([]Result{}, j.Results...)
	sort.SliceStable(j.Results, func(i, k int) bool {
		if (j.Results[i].Error == "") != (j.Results[k].Error == "") {
			return j.Results[i].Error == ""
		}
		return j.Results[i].Mbps > j.Results[k].Mbps
	})
	return j
}
func (m *Manager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshot(), m.job.ID == id && id != ""
}
func (m *Manager) Cancel(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job.ID != id || id == "" {
		return Job{}, false
	}
	if m.job.State == "running" {
		m.cancel()
	}
	return m.snapshot(), true
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
}
func (m *Manager) run(ctx context.Context, id, target string, ips []string, limits config.Test) {
	tasks := make(chan string)
	var wg sync.WaitGroup
	for n := 0; n < limits.Concurrency; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range tasks {
				if ctx.Err() != nil {
					return
				}
				res := m.probe(ctx, ip, target, limits)
				m.mu.Lock()
				if m.job.ID == id {
					m.job.Results = append(m.job.Results, res)
					m.job.Completed++
				}
				m.mu.Unlock()
			}
		}()
	}
loop:
	for _, ip := range ips {
		select {
		case tasks <- ip:
		case <-ctx.Done():
			break loop
		}
	}
	close(tasks)
	wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job.ID == id {
		m.job.State = "done"
		if ctx.Err() != nil {
			m.job.State = "cancelled"
		}
		m.cancel()
	}
}
func Measure(parent context.Context, ip, target string, limits config.Test) Result {
	result := Result{IP: ip}
	ctx, cancel := context.WithTimeout(parent, time.Duration(limits.TimeoutSeconds)*time.Second)
	defer cancel()
	u, e := url.Parse(target)
	if e != nil {
		result.Error = e.Error()
		return result
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: time.Duration(limits.TimeoutSeconds) * time.Second, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, e := net.SplitHostPort(addr)
		if e != nil {
			return nil, e
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
	}}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		result.Error = e.Error()
		return result
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", limits.MaxBytes-1))
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "XboxDownloadProxy/0.1")
	start := time.Now()
	res, e := client.Do(req)
	result.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
	if e != nil {
		result.Error = e.Error()
		if parent.Err() != nil {
			result.Error = "cancelled"
		}
		return result
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 206 {
		result.Error = fmt.Sprintf("HTTP %d (redirects are not followed)", res.StatusCode)
		return result
	}
	if res.StatusCode == 206 && !validContentRange(res.Header.Get("Content-Range")) {
		result.Error = "invalid Content-Range for requested offset 0"
		return result
	}
	n, e := io.Copy(io.Discard, io.LimitReader(res.Body, limits.MaxBytes))
	result.Bytes = n
	result.Seconds = time.Since(start).Seconds()
	if result.Seconds > 0 {
		result.Mbps = float64(n) * 8 / 1e6 / result.Seconds
	}
	if parent.Err() != nil {
		result.Error = "cancelled"
	} else if e != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Error = e.Error()
	} else if n == 0 {
		result.Error = "empty response"
	} else if e != nil && n < 65536 {
		result.Error = "timed out before receiving enough test data"
	}
	return result
}
func validContentRange(s string) bool {
	var start, end int64
	_, e := fmt.Sscanf(s, "bytes %d-%d/", &start, &end)
	return e == nil && start == 0 && end >= start
}
