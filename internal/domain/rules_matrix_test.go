package domain

import (
	"testing"

	"xboxdownloadproxy/internal/config"
)

func domainConfigForTest() config.Config {
	c := config.Default()
	c.LANIP = "192.168.50.4"
	c.ShowLinks = true
	c.RedirectToCN = true
	c.OfflineEnabled = false
	c.Groups = map[string]config.Group{
		"global": {IP: "8.8.8.8"},
		"cn1":    {IP: "8.8.4.4"},
		"cn2":    {IP: "1.1.1.1"},
		"store":  {IP: "9.9.9.9"},
	}
	return c
}

func TestGroupForMatchesOnlyDeclaredHosts(t *testing.T) {
	tests := []struct{ host, want string }{
		{"assets1.xboxlive.com", "global"}, {"D2.XBOXLIVE.COM.", "global"},
		{"assets2.xboxlive.cn", "cn1"}, {"d1.xboxlive.cn", "cn1"},
		{"dlassets2.xboxlive.cn", "cn2"},
		{"dl.delivery.mp.microsoft.com", "store"}, {"1d.tlu.dl.delivery.mp.microsoft.com", "store"},
		{"xboxlive.com", ""}, {"assets1.xboxlive.com.evil.test", ""},
		{"notassets1.xboxlive.com", ""}, {"assets1.xboxlive.cn..", ""},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := GroupFor(tt.host); got != tt.want {
				t.Fatalf("GroupFor(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

func TestEveryDeclaredHostHasItsExpectedGroup(t *testing.T) {
	want := map[string]string{
		"assets1.xboxlive.com": "global", "assets2.xboxlive.com": "global",
		"xvcf1.xboxlive.com": "global", "xvcf2.xboxlive.com": "global",
		"d1.xboxlive.com": "global", "d2.xboxlive.com": "global",
		"dlassets.xboxlive.com": "global", "dlassets2.xboxlive.com": "global",
		"assets1.xboxlive.cn": "cn1", "assets2.xboxlive.cn": "cn1",
		"d1.xboxlive.cn": "cn1", "d2.xboxlive.cn": "cn1",
		"dlassets.xboxlive.cn": "cn2", "dlassets2.xboxlive.cn": "cn2",
		"dl.delivery.mp.microsoft.com": "store", "tlu.dl.delivery.mp.microsoft.com": "store",
		"1d.tlu.dl.delivery.mp.microsoft.com": "store",
	}
	for host, expectedGroup := range want {
		if got := GroupFor(host); got != expectedGroup {
			t.Errorf("GroupFor(%q) = %q, want %q", host, got, expectedGroup)
		}
	}
}

func TestAddressRedirectAndControlledOptions(t *testing.T) {
	tests := []struct {
		name                    string
		edit                    func(*config.Config)
		host, address, redirect string
		controlled              bool
	}{
		{"first linked CDN alias", func(*config.Config) {}, "xvcf1.xboxlive.com", "192.168.50.4", "assets2.xboxlive.com", true},
		{"second linked global alias", func(*config.Config) {}, "assets2.xboxlive.com", "8.8.8.8", "", true},
		{"first domestic alias", func(*config.Config) {}, "assets1.xboxlive.cn", "192.168.50.4", "assets2.xboxlive.cn", true},
		{"second domestic alias", func(*config.Config) {}, "assets2.xboxlive.cn", "8.8.4.4", "", true},
		{"legacy domestic first alias", func(*config.Config) {}, "dlassets.xboxlive.cn", "192.168.50.4", "dlassets2.xboxlive.cn", true},
		{"store uses configured IP", func(*config.Config) {}, "dl.delivery.mp.microsoft.com", "9.9.9.9", "", true},
		{"links disabled uses group IP", func(c *config.Config) { c.ShowLinks = false }, "assets1.xboxlive.cn", "8.8.4.4", "", true},
		{"links disabled preserves second-alias redirect", func(c *config.Config) { c.ShowLinks = false }, "assets2.xboxlive.com", "8.8.8.8", "", true},
		{"unchanged global host needs no redirect", func(c *config.Config) { c.RedirectToCN = false; c.ShowLinks = false }, "assets1.xboxlive.com", "8.8.8.8", "", true},
		{"global fallback to LAN when redirect-to-CN enabled", func(c *config.Config) { g := c.Groups["global"]; g.IP = ""; c.Groups["global"] = g }, "assets1.xboxlive.com", "192.168.50.4", "assets2.xboxlive.cn", true},
		{"no global fallback when redirect-to-CN disabled", func(c *config.Config) {
			c.RedirectToCN = false
			c.ShowLinks = false
			g := c.Groups["global"]
			g.IP = ""
			c.Groups["global"] = g
		}, "assets1.xboxlive.com", "", "", false},
		{"offline forces local Xbox CDN address", func(c *config.Config) { c.OfflineEnabled = true }, "assets2.xboxlive.cn", "192.168.50.4", "", true},
		{"offline does not redirect Store", func(c *config.Config) { c.OfflineEnabled = true }, "dl.delivery.mp.microsoft.com", "9.9.9.9", "", true},
		{"unknown suffix is untouched", func(*config.Config) {}, "assets1.xboxlive.com.evil.test", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := domainConfigForTest()
			tt.edit(&c)
			if got := Address(c, tt.host); got != tt.address {
				t.Errorf("Address() = %q, want %q", got, tt.address)
			}
			if got := Redirect(c, tt.host); got != tt.redirect {
				t.Errorf("Redirect() = %q, want %q", got, tt.redirect)
			}
			if got := Controlled(c, tt.host); got != tt.controlled {
				t.Errorf("Controlled() = %v, want %v", got, tt.controlled)
			}
		})
	}
}

func TestOfflineOverridesConfiguredIPsForDownloadGroups(t *testing.T) {
	c := domainConfigForTest()
	c.OfflineEnabled = true
	for _, host := range []string{"assets1.xboxlive.com", "dlassets2.xboxlive.cn"} {
		if got := Address(c, host); got != c.LANIP {
			t.Errorf("Address(%q) = %q, want local %q", host, got, c.LANIP)
		}
	}
	if got := Address(c, "1d.tlu.dl.delivery.mp.microsoft.com"); got != "9.9.9.9" {
		t.Fatalf("Store address = %q, want configured address", got)
	}
}

func TestLegacyGlobalRedirectOptions(t *testing.T) {
	tests := []struct {
		name, host, want string
		showLinks, redirectToCN bool
		globalIP string
	}{
		{"first legacy host with links to CN", "dlassets.xboxlive.com", "dlassets2.xboxlive.cn", true, true, ""},
		{"second legacy host with links changes stem", "dlassets2.xboxlive.com", "dlassets2.xboxlive.cn", true, true, ""},
		{"first legacy host without links changes region", "dlassets.xboxlive.com", "dlassets.xboxlive.cn", false, true, ""},
		{"legacy first alias with configured IP stays in global region", "dlassets.xboxlive.com", "dlassets2.xboxlive.com", true, true, "8.8.8.8"},
		{"legacy second alias already at destination", "dlassets2.xboxlive.com", "", false, false, "8.8.8.8"},
		{"legacy redirects retain global region when CN redirect disabled", "dlassets.xboxlive.com", "dlassets2.xboxlive.com", true, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := domainConfigForTest()
			c.ShowLinks, c.RedirectToCN = tt.showLinks, tt.redirectToCN
			g := c.Groups["global"]
			g.IP = tt.globalIP
			c.Groups["global"] = g
			if got := Redirect(c, tt.host); got != tt.want {
				t.Fatalf("Redirect(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}
