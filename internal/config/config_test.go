package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistenceAndValidation(t *testing.T) {
	dir := t.TempDir()
	c := Default()
	if c.OfflineEnabled {
		t.Fatal("offline downloads must require opt-in")
	}
	if got, want := c.Groups["cn2"].TestURL, "http://assets2.xboxlive.cn/Z/routing/extraextralarge.txt"; got != want {
		t.Fatalf("default cn2 test URL = %q, want %q", got, want)
	}
	if e := Save(dir, c); e != nil {
		t.Fatal(e)
	}
	token, e := Token(dir)
	if e != nil {
		t.Fatal(e)
	}
	again, e := Token(dir)
	if e != nil || token != again {
		t.Fatal("token changed")
	}
	info, _ := os.Stat(filepath.Join(dir, "token"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("token permissions")
	}
	got, e := Load(dir)
	if e != nil || got.LANIP != c.LANIP {
		t.Fatal(e)
	}
	bad := Clone(c)
	bad.Groups["store"] = Group{IP: "127.0.0.1"}
	if e = Save(dir, bad); e == nil {
		t.Fatal("bad IP accepted")
	}
	got, e = Load(dir)
	if e != nil || got.Groups["store"].IP != "" {
		t.Fatal("failed update corrupted config")
	}
	bad = c
	bad.UpstreamDNS = c.LANIP + ":53"
	if e = Validate(bad); e == nil {
		t.Fatal("DNS recursion accepted")
	}
	bad = Clone(c)
	bad.OfflineEnabled, bad.TLSListen = true, ""
	if err := Validate(bad); err == nil {
		t.Fatal("local DNS routing would break HTTPS without a TLS listener")
	}
}

func TestLoadBackfillsEmptyCN2TestURLAndPreservesCustomURL(t *testing.T) {
	const defaultCN2TestURL = "http://assets2.xboxlive.cn/Z/routing/extraextralarge.txt"
	dir := t.TempDir()
	c := Default()
	c.Groups["cn2"] = Group{SourceURL: c.Groups["cn2"].SourceURL}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups["cn2"].TestURL != defaultCN2TestURL {
		t.Fatalf("empty cn2 test URL = %q, want backfilled %q", got.Groups["cn2"].TestURL, defaultCN2TestURL)
	}

	const customURL = "https://cdn.example.test/game/test.bin"
	c.Groups["cn2"] = Group{SourceURL: c.Groups["cn2"].SourceURL, TestURL: customURL}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}

	got, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups["cn2"].TestURL != customURL {
		t.Fatalf("custom cn2 test URL = %q, want preserved %q", got.Groups["cn2"].TestURL, customURL)
	}
}
