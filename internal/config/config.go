package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type Group struct {
	IP        string `json:"ip"`
	SourceURL string `json:"sourceURL"`
	TestURL   string `json:"testURL"`
}
type Test struct {
	TimeoutSeconds int   `json:"timeoutSeconds"`
	MaxBytes       int64 `json:"maxBytes"`
	Concurrency    int   `json:"concurrency"`
}
type Config struct {
	SchemaVersion  int              `json:"schemaVersion"`
	LANIP          string           `json:"lanIP"`
	DNSListen      string           `json:"dnsListen"`
	HTTPListen     string           `json:"httpListen"`
	TLSListen      string           `json:"tlsListen"`
	UpstreamDNS    string           `json:"upstreamDNS"`
	WebListen      string           `json:"webListen"`
	ShowLinks      bool             `json:"showLinks"`
	OfflineEnabled bool             `json:"offlineEnabled"`
	RedirectToCN   bool             `json:"redirectToCN"`
	SuppressAAAA   bool             `json:"suppressAAAA"`
	AutoStart      bool             `json:"autoStart"`
	Groups         map[string]Group `json:"groups"`
	Test           Test             `json:"test"`
}

func Default() Config {
	base := "https://raw.githubusercontent.com/skydevil88/XboxDownload/master/IP/IP."
	return Config{SchemaVersion: 1, LANIP: DetectLANIP(), DNSListen: ":53", HTTPListen: ":80", TLSListen: ":443", UpstreamDNS: "114.114.114.114:53", WebListen: ":8080", ShowLinks: true, RedirectToCN: true, SuppressAAAA: true,
		Groups: map[string]Group{
			"global": {SourceURL: base + "Akamai.txt", TestURL: "http://assets1.xboxlive.com/Z/routing/extraextralarge.txt"},
			"cn1":    {SourceURL: base + "XboxCn1.txt", TestURL: "http://assets1.xboxlive.cn/Z/routing/extraextralarge.txt"},
			"cn2":    {SourceURL: base + "XboxCn2.txt", TestURL: "http://assets2.xboxlive.cn/Z/routing/extraextralarge.txt"},
			"store":  {SourceURL: base + "XboxApp.txt"},
		}, Test: Test{TimeoutSeconds: 5, MaxBytes: 10 * 1024 * 1024, Concurrency: 2}}
}

func DetectLANIP() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ip, _, err := net.ParseCIDR(a.String())
		if err == nil && ip.To4() != nil && ip.IsPrivate() {
			return ip.String()
		}
	}
	return "127.0.0.1"
}
func DefaultDir() string {
	d, e := os.UserConfigDir()
	if e != nil {
		return "./data"
	}
	return filepath.Join(d, "xboxdownloadproxy")
}
func PublicIPv4(s string) bool {
	ip, e := netip.ParseAddr(s)
	return e == nil && ip.Is4() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}
func Validate(c Config) error {
	if c.SchemaVersion != 1 {
		return errors.New("unsupported schemaVersion (expected 1)")
	}
	if c.OfflineEnabled && c.TLSListen == "" {
		return errors.New("offline delivery requires tlsListen to preserve HTTPS forwarding for Xbox domains routed to this server")
	}
	ip := net.ParseIP(c.LANIP)
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("lanIP must be the server's reachable IPv4 LAN address")
	}
	addresses := map[string]string{"dnsListen": c.DNSListen, "httpListen": c.HTTPListen, "webListen": c.WebListen}
	if c.TLSListen != "" {
		addresses["tlsListen"] = c.TLSListen
	}
	seen := map[string]string{}
	for key, value := range addresses {
		host, port, err := net.SplitHostPort(value)
		if err != nil || port == "" {
			return fmt.Errorf("%s: expected IP:port or :port", key)
		}
		if host != "" && net.ParseIP(host) == nil {
			return fmt.Errorf("%s: use a numeric bind IP", key)
		}
		n, err := net.LookupPort("tcp", port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%s: invalid port", key)
		}
		normalized := net.JoinHostPort(host, port)
		if prev, ok := seen[normalized]; ok {
			return fmt.Errorf("%s conflicts with %s", key, prev)
		}
		seen[normalized] = key
	}
	host, port, err := net.SplitHostPort(c.UpstreamDNS)
	if err != nil || net.ParseIP(host) == nil || port == "" {
		return errors.New("upstreamDNS must be a numeric IP:port")
	}
	if n, err := net.LookupPort("udp", port); err != nil || n < 1 || n > 65535 {
		return errors.New("upstreamDNS: invalid port")
	}
	dh, dp, _ := net.SplitHostPort(c.DNSListen)
	if dp == port && (host == c.LANIP || host == dh || (net.ParseIP(host).IsLoopback() && (dh == "" || net.ParseIP(dh).IsLoopback()))) {
		return errors.New("upstreamDNS points back to this DNS listener")
	}
	if len(c.Groups) != 4 {
		return errors.New("exactly global, cn1, cn2 and store groups are required")
	}
	for _, key := range []string{"global", "cn1", "cn2", "store"} {
		g, ok := c.Groups[key]
		if !ok {
			return fmt.Errorf("missing group %s", key)
		}
		if g.IP != "" && !PublicIPv4(g.IP) {
			return fmt.Errorf("%s.ip: expected a public CDN IPv4 address", key)
		}
		if g.SourceURL != "" {
			u, e := url.Parse(g.SourceURL)
			if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
				return fmt.Errorf("%s.sourceURL must be an HTTPS URL", key)
			}
		}
		if g.TestURL != "" {
			if err := ValidateTestURL(g.TestURL); err != nil {
				return fmt.Errorf("%s.testURL: %w", key, err)
			}
		}
	}
	if c.Test.TimeoutSeconds < 1 || c.Test.TimeoutSeconds > 30 || c.Test.MaxBytes < 1024 || c.Test.MaxBytes > 50*1024*1024 || c.Test.Concurrency < 1 || c.Test.Concurrency > 8 {
		return errors.New("test limits: timeoutSeconds 1..30, maxBytes 1KiB..50MiB, concurrency 1..8")
	}
	return nil
}
func ValidateTestURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("expected an HTTP(S) download URL without credentials or fragment")
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return errors.New("test URL must use port 80 or 443")
	}
	return nil
}
func Load(dir string) (Config, error) {
	b, e := os.ReadFile(filepath.Join(dir, "config.json"))
	if e != nil {
		return Config{}, e
	}
	var c Config
	if e = json.Unmarshal(b, &c); e != nil {
		return c, fmt.Errorf("config.json: %w", e)
	}
	if err := Validate(c); err != nil {
		return c, err
	}
	if cn2, ok := c.Groups["cn2"]; ok && cn2.TestURL == "" {
		cn2.TestURL = "http://assets2.xboxlive.cn/Z/routing/extraextralarge.txt"
		c.Groups["cn2"] = cn2
	}
	return c, nil
}
func Save(dir string, c Config) error {
	if e := Validate(c); e != nil {
		return e
	}
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	return AtomicWrite(filepath.Join(dir, "config.json"), append(b, '\n'))
}
func AtomicWrite(path string, data []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".xdp-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	// Preserve the user's existing config ownership when serve is started via
	// sudo to bind low ports. Otherwise a later unprivileged CLI cannot read it.
	if os.Geteuid() == 0 {
		if info, err := os.Stat(path); err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				if err := f.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
					f.Close()
					return err
				}
			}
		}
	}
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(name, path)
}
func Token(dir string) (string, error) {
	path := filepath.Join(dir, "token")
	b, e := os.ReadFile(path)
	if e == nil {
		s := strings.TrimSpace(string(b))
		if len(s) < 32 {
			return "", errors.New("invalid management token file")
		}
		return s, nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	b = make([]byte, 32)
	if _, e = rand.Read(b); e != nil {
		return "", e
	}
	s := hex.EncodeToString(b)
	return s, AtomicWrite(path, []byte(s+"\n"))
}
func Clone(c Config) Config {
	g := make(map[string]Group, len(c.Groups))
	for k, v := range c.Groups {
		g[k] = v
	}
	c.Groups = g
	return c
}
