package candidates

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"xboxdownloadproxy/internal/config"
	"xboxdownloadproxy/internal/domain"
	"xboxdownloadproxy/internal/engine"
)

type Item struct {
	IP       string `json:"ip"`
	Location string `json:"location"`
}
type List struct {
	Group     string    `json:"group"`
	Items     []Item    `json:"items"`
	Origin    string    `json:"origin"`
	Warning   string    `json:"warning,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	SourceURL string    `json:"sourceURL,omitempty"`
}
type Store struct {
	mu     sync.Mutex
	Dir    string
	Client *http.Client
}

func New(dir string) *Store {
	return &Store{Dir: dir, Client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 4 || req.URL.Scheme != "https" {
			return errors.New("candidate source redirect rejected")
		}
		return nil
	}}}
}
func Parse(text string) ([]Item, error) {
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 4096), 65536)
	out := []Item{}
	seen := map[string]bool{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		ip := fields[0]
		if !config.PublicIPv4(ip) || seen[ip] {
			continue
		}
		seen[ip] = true
		loc := ""
		if len(fields) > 1 {
			loc = strings.TrimSpace(strings.TrimPrefix(line, ip))
		}
		out = append(out, Item{ip, loc})
		if len(out) > 20000 {
			return nil, errors.New("candidate list exceeds 20000 IPs")
		}
	}
	if e := scanner.Err(); e != nil {
		return nil, e
	}
	if len(out) == 0 {
		return nil, errors.New("list contains no public IPv4 addresses (expected IP followed by optional location)")
	}
	return out, nil
}
func (s *Store) path(group string) string { return filepath.Join(s.Dir, "candidates-"+group+".json") }
func validGroup(group string) bool {
	for _, g := range domain.Groups {
		if g.ID == group {
			return true
		}
	}
	return false
}
func (s *Store) Import(group, text string) (List, error) {
	if !validGroup(group) {
		return List{}, errors.New("unknown group")
	}
	items, e := Parse(text)
	if e != nil {
		return List{}, e
	}
	l := List{Group: group, Items: items, Origin: "import", UpdatedAt: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	return l, s.save(l)
}
func (s *Store) save(l List) error {
	b, e := json.Marshal(l)
	if e != nil {
		return e
	}
	return config.AtomicWrite(s.path(l.Group), b)
}
func (s *Store) Load(ctx context.Context, c config.Config, group string, refresh bool) (List, error) {
	if !validGroup(group) {
		return List{}, errors.New("unknown group")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var cached List
	b, _ := os.ReadFile(s.path(group))
	_ = json.Unmarshal(b, &cached)
	source := c.Groups[group].SourceURL
	if !refresh && len(cached.Items) > 0 && (cached.Origin == "import" || (time.Since(cached.UpdatedAt) < 24*time.Hour && cached.SourceURL == source)) {
		cached.Origin = "cache"
		return cached, nil
	}
	l, e := s.download(ctx, source, group)
	if e == nil {
		if e = s.save(l); e != nil {
			return List{}, e
		}
		return l, nil
	}
	if ctx.Err() != nil {
		return List{}, ctx.Err()
	}
	if len(cached.Items) > 0 {
		cached.Origin = "cache"
		cached.Warning = "在线来源不可用，使用缓存：" + e.Error()
		return cached, nil
	}
	var host string
	for _, g := range domain.Groups {
		if g.ID == group {
			host = g.Hosts[0]
			break
		}
	}
	ips, dnsErr := engine.ResolveIPv4(ctx, host, c.UpstreamDNS)
	if dnsErr != nil {
		return List{}, fmt.Errorf("source unavailable: %v; DNS fallback failed: %w", e, dnsErr)
	}
	l = List{Group: group, Items: []Item{}, Origin: "dns", Warning: "在线候选源不可用，临时使用当前 DNS 结果，优选范围有限：" + e.Error(), UpdatedAt: time.Now().UTC()}
	for _, ip := range ips {
		l.Items = append(l.Items, Item{ip, "DNS " + host})
	}
	return l, nil
}
func (s *Store) download(ctx context.Context, source, group string) (List, error) {
	if source == "" {
		return List{}, errors.New("no candidate source URL configured")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if e != nil {
		return List{}, e
	}
	res, e := s.Client.Do(req)
	if e != nil {
		return List{}, e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return List{}, fmt.Errorf("candidate source HTTP %d", res.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 4*1024*1024+1))
	if e != nil {
		return List{}, e
	}
	if len(b) > 4*1024*1024 {
		return List{}, errors.New("candidate source exceeds 4MiB")
	}
	items, e := Parse(string(b))
	if e != nil {
		return List{}, e
	}
	return List{Group: group, Items: items, Origin: "online", UpdatedAt: time.Now().UTC(), SourceURL: source}, nil
}
