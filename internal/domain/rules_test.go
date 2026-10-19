package domain

import (
	"testing"
	"xboxdownloadproxy/internal/config"
)

func TestReferencePaths(t *testing.T) {
	c := config.Default()
	c.LANIP = "192.168.1.10"
	g := c.Groups["cn1"]
	g.IP = "183.61.239.178"
	c.Groups["cn1"] = g
	for _, tc := range []struct{ host, ip, dest string }{
		{"xvcf1.xboxlive.com", c.LANIP, "assets2.xboxlive.cn"},
		{"assets1.xboxlive.cn", c.LANIP, "assets2.xboxlive.cn"},
		{"assets2.xboxlive.cn", g.IP, ""},
		{"dl.delivery.mp.microsoft.com", "", ""},
		{"assets1.xboxlive.com.evil.test", "", ""},
	} {
		if got := Address(c, tc.host); got != tc.ip {
			t.Errorf("%s address %q, want %q", tc.host, got, tc.ip)
		}
		if got := Redirect(c, tc.host); got != tc.dest {
			t.Errorf("%s redirect %q, want %q", tc.host, got, tc.dest)
		}
	}
	c.ShowLinks = false
	if got := Address(c, "assets1.xboxlive.cn"); got != g.IP {
		t.Fatal(got)
	}
	if got := Redirect(c, "assets1.xboxlive.com"); got != "assets1.xboxlive.cn" {
		t.Fatal(got)
	}
	g = c.Groups["global"]
	g.IP = "1.1.1.1"
	c.Groups["global"] = g
	if got := Address(c, "assets1.xboxlive.com"); got != g.IP {
		t.Fatal(got)
	}
	c.RedirectToCN = false
	g.IP = ""
	c.Groups["global"] = g
	if got := Address(c, "assets1.xboxlive.com"); got != "" {
		t.Fatal(got)
	}
}
