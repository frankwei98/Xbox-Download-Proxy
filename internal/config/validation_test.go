package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfigForTest() Config {
	c := Default()
	c.LANIP = "192.168.1.20"
	c.DNSListen = ":5353"
	c.HTTPListen = ":8081"
	c.TLSListen = ":8443"
	c.WebListen = ":8080"
	c.UpstreamDNS = "1.1.1.1:53"
	return c
}

func TestValidateSchemaAndLANIP(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"supported schema", func(*Config) {}, ""},
		{"old schema", func(c *Config) { c.SchemaVersion = 0 }, "unsupported schemaVersion"},
		{"future schema", func(c *Config) { c.SchemaVersion = 2 }, "unsupported schemaVersion"},
		{"valid private IPv4", func(*Config) {}, ""},
		{"IPv6", func(c *Config) { c.LANIP = "2001:db8::1" }, "lanIP"},
		{"malformed", func(c *Config) { c.LANIP = "192.168.1" }, "lanIP"},
		{"unspecified", func(c *Config) { c.LANIP = "0.0.0.0" }, "lanIP"},
		{"multicast", func(c *Config) { c.LANIP = "239.1.2.3" }, "lanIP"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfigForTest()
			tt.edit(&c)
			err := Validate(c)
			if tt.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateListenAddressesAndPorts(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"numeric bind IPv4", func(c *Config) { c.WebListen = "192.168.1.20:8080" }, ""},
		{"empty TLS disables listener", func(c *Config) { c.TLSListen = "" }, ""},
		{"malformed listen", func(c *Config) { c.HTTPListen = "8081" }, "httpListen"},
		{"hostname bind rejected", func(c *Config) { c.WebListen = "localhost:8080" }, "numeric bind IP"},
		{"invalid port", func(c *Config) { c.DNSListen = ":65536" }, "dnsListen"},
		{"zero port", func(c *Config) { c.DNSListen = ":0" }, "dnsListen"},
		{"same wildcard endpoint conflicts", func(c *Config) { c.WebListen = ":5353" }, "conflicts"},
		{"distinct numeric bind IPs may share a port", func(c *Config) { c.DNSListen = "127.0.0.1:5353"; c.WebListen = "127.0.0.2:5353" }, ""},
		{"offline requires TLS", func(c *Config) { c.OfflineEnabled, c.TLSListen = true, "" }, "offline delivery requires tlsListen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfigForTest()
			tt.edit(&c)
			err := Validate(c)
			if tt.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateUpstreamDNS(t *testing.T) {
	tests := []struct {
		name, value, lan, dns string
		want                  string
	}{
		{"valid address", "8.8.8.8:53", "192.168.1.20", ":5353", ""},
		{"hostname rejected", "resolver.example:53", "192.168.1.20", ":5353", "numeric IP"},
		{"missing port", "8.8.8.8", "192.168.1.20", ":5353", "numeric IP"},
		{"invalid port", "8.8.8.8:70000", "192.168.1.20", ":5353", "invalid port"},
		{"points at LAN listener", "192.168.1.20:53", "192.168.1.20", ":53", "points back"},
		{"same loopback listener", "127.0.0.1:5353", "192.168.1.20", ":5353", "points back"},
		{"other upstream port", "127.0.0.1:53", "192.168.1.20", ":5353", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfigForTest()
			c.UpstreamDNS, c.LANIP, c.DNSListen = tt.value, tt.lan, tt.dns
			err := Validate(c)
			if tt.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateGroups(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"all required groups", func(*Config) {}, ""},
		{"missing group", func(c *Config) { delete(c.Groups, "store") }, "exactly"},
		{"unknown replacing required group", func(c *Config) { delete(c.Groups, "store"); c.Groups["other"] = Group{} }, "missing group store"},
		{"private CDN address", func(c *Config) { g := c.Groups["cn1"]; g.IP = "10.0.0.1"; c.Groups["cn1"] = g }, "cn1.ip"},
		{"public CDN address", func(c *Config) { g := c.Groups["cn1"]; g.IP = "8.8.8.8"; c.Groups["cn1"] = g }, ""},
		{"non-HTTPS source", func(c *Config) {
			g := c.Groups["cn1"]
			g.SourceURL = "http://cdn.example.test/list"
			c.Groups["cn1"] = g
		}, "cn1.sourceURL"},
		{"source credentials", func(c *Config) {
			g := c.Groups["cn1"]
			g.SourceURL = "https://user:pass@cdn.example.test/list"
			c.Groups["cn1"] = g
		}, "cn1.sourceURL"},
		{"invalid test URL", func(c *Config) { g := c.Groups["cn1"]; g.TestURL = "ftp://cdn.example.test/file"; c.Groups["cn1"] = g }, "cn1.testURL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfigForTest()
			tt.edit(&c)
			err := Validate(c)
			if tt.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateTestLimitsBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		timeout     int
		bytes       int64
		concurrency int
		valid       bool
	}{
		{"lower bounds", 1, 1024, 1, true}, {"upper bounds", 30, 50 * 1024 * 1024, 8, true},
		{"timeout below", 0, 1024, 1, false}, {"timeout above", 31, 1024, 1, false},
		{"bytes below", 1, 1023, 1, false}, {"bytes above", 1, 50*1024*1024 + 1, 1, false},
		{"concurrency below", 1, 1024, 0, false}, {"concurrency above", 1, 1024, 9, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfigForTest()
			c.Test = Test{TimeoutSeconds: tt.timeout, MaxBytes: tt.bytes, Concurrency: tt.concurrency}
			err := Validate(c)
			if tt.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !tt.valid && (err == nil || !strings.Contains(err.Error(), "test limits")) {
				t.Fatalf("Validate() error = %v, want test limits error", err)
			}
		})
	}
}

func TestValidateTestURLCases(t *testing.T) {
	for _, tt := range []struct {
		value string
		valid bool
	}{
		{"http://cdn.example.test/file", true}, {"https://cdn.example.test/file", true},
		{"https://cdn.example.test:443/file", true}, {"http://cdn.example.test:80/file", true},
		{"https://cdn.example.test:8443/file", false}, {"file:///tmp/a", false}, {"//cdn.example.test/file", false},
		{"https://user@cdn.example.test/file", false}, {"https://cdn.example.test/file#part", false},
		{"https://cdn.example.test:bad/file", false}, {"https:///file", false},
	} {
		t.Run(tt.value, func(t *testing.T) {
			err := ValidateTestURL(tt.value)
			if (err == nil) != tt.valid {
				t.Fatalf("ValidateTestURL(%q) error = %v", tt.value, err)
			}
		})
	}
}

func TestLoadErrorPathsAndValidation(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		_, err := Load(t.TempDir())
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Load() error = %v, want not-exist", err)
		}
	})
	t.Run("directory path", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(file)
		if err == nil {
			t.Fatal("Load() succeeded through a regular file path")
		}
	})
	t.Run("invalid JSON", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(dir)
		if err == nil || !strings.Contains(err.Error(), "config.json:") {
			t.Fatalf("Load() error = %v", err)
		}
	})
	t.Run("invalid config", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"schemaVersion":2}`), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(dir)
		if err == nil || !strings.Contains(err.Error(), "schemaVersion") {
			t.Fatalf("Load() error = %v", err)
		}
	})
}

func TestAtomicWriteCreatesPrivateFileAndReplacesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	if err := AtomicWrite(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("contents = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestAtomicWriteReportsPathTypeConflict(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "file")
	if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	err := AtomicWrite(filepath.Join(parent, "config.json"), []byte("data"))
	if err == nil {
		t.Fatal("AtomicWrite succeeded with a regular file as parent")
	}
}

func TestTokenCreationReuseAndInvalidFile(t *testing.T) {
	t.Run("creates cryptographic token and reuses it", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "new", "data")
		first, err := Token(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(first) != 64 {
			t.Fatalf("generated token has %d characters, want 64", len(first))
		}
		for _, r := range first {
			if !strings.ContainsRune("0123456789abcdef", r) {
				t.Fatalf("generated token contains non-hex character %q", r)
			}
		}
		second, err := Token(dir)
		if err != nil || second != first {
			t.Fatalf("second Token() = %q, %v; want original token", second, err)
		}
	})
	t.Run("rejects short existing token", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "token"), []byte("short\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Token(dir); err == nil || !strings.Contains(err.Error(), "invalid management token") {
			t.Fatalf("Token() error = %v, want invalid-token error", err)
		}
	})
	t.Run("reports path type conflict", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Token(file); err == nil {
			t.Fatal("Token() succeeded when its directory path is a regular file")
		}
	})
}

func TestCloneCopiesGroupsIndependently(t *testing.T) {
	original := validConfigForTest()
	cloned := Clone(original)
	group := cloned.Groups["global"]
	group.IP = "8.8.8.8"
	cloned.Groups["global"] = group
	delete(cloned.Groups, "store")
	cloned.Groups["extra"] = Group{IP: "1.1.1.1"}
	if original.Groups["global"].IP != "" {
		t.Fatalf("mutating clone changed original group: %+v", original.Groups["global"])
	}
	if _, ok := original.Groups["store"]; !ok {
		t.Fatal("deleting from clone removed original group")
	}
	if _, ok := original.Groups["extra"]; ok {
		t.Fatal("adding to clone changed original groups")
	}
}

func TestAtomicWriteRenameFailurePreservesTargetAndCleansTemporaryFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "existing-directory")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	err := AtomicWrite(target, []byte("replacement"))
	if err == nil {
		t.Fatal("AtomicWrite unexpectedly replaced a directory")
	}
	got, readErr := os.ReadFile(marker)
	if readErr != nil || string(got) != "keep" {
		t.Fatalf("existing directory content after failure = %q, %v", got, readErr)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != "existing-directory" {
		t.Fatalf("temporary file remained after failed rename; entries = %v", entries)
	}
}

func TestTokenGeneratesDistinctValuesForSeparateDirectories(t *testing.T) {
	first, err := Token(filepath.Join(t.TempDir(), "first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Token(filepath.Join(t.TempDir(), "second"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("independent token generation returned the same value")
	}
}
