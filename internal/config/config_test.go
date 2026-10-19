package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistenceAndValidation(t *testing.T) {
	dir := t.TempDir()
	c := Default()
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
}
