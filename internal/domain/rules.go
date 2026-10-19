package domain

import (
	"strings"
	"xboxdownloadproxy/internal/config"
)

type Group struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Hosts     []string `json:"hosts"`
	SourceURL string   `json:"sourceURL"`
	TestURL   string   `json:"testURL"`
}

var Groups = []Group{
	{ID: "global", Label: "Xbox 全球 / Akamai", Hosts: []string{"assets1.xboxlive.com", "assets2.xboxlive.com", "xvcf1.xboxlive.com", "xvcf2.xboxlive.com", "d1.xboxlive.com", "d2.xboxlive.com", "dlassets.xboxlive.com", "dlassets2.xboxlive.com"}},
	{ID: "cn1", Label: "Xbox 国内主下载", Hosts: []string{"assets1.xboxlive.cn", "assets2.xboxlive.cn", "d1.xboxlive.cn", "d2.xboxlive.cn"}},
	{ID: "cn2", Label: "Xbox 国内旧下载", Hosts: []string{"dlassets.xboxlive.cn", "dlassets2.xboxlive.cn"}},
	{ID: "store", Label: "Microsoft Store", Hosts: []string{"dl.delivery.mp.microsoft.com", "tlu.dl.delivery.mp.microsoft.com", "1d.tlu.dl.delivery.mp.microsoft.com"}},
}

func Normalize(host string) string { return strings.TrimSuffix(strings.ToLower(host), ".") }
func GroupFor(host string) string {
	host = Normalize(host)
	for _, g := range Groups {
		for _, h := range g.Hosts {
			if h == host {
				return g.ID
			}
		}
	}
	return ""
}
func first(host string) bool { return !strings.Contains(strings.Split(host, ".")[0], "2") }

// LocalDeliveryHost covers Xbox game CDN aliases. Store URLs and HTTPS file
// delivery are deliberately outside this HTTP-only feature.
func LocalDeliveryHost(host string) bool {
	key := GroupFor(host)
	return key == "global" || key == "cn1" || key == "cn2"
}

// Address chooses the CDN IP, or the local HTTP redirect listener, for an A query.
// An empty address means preserve the upstream DNS answer.
func Address(c config.Config, host string) string {
	host = Normalize(host)
	key := GroupFor(host)
	if key == "" {
		return ""
	}
	if c.OfflineEnabled && LocalDeliveryHost(host) {
		return c.LANIP
	}
	g := c.Groups[key]
	if key == "store" {
		return g.IP
	}
	if key == "global" && c.RedirectToCN && g.IP == "" {
		return c.LANIP
	}
	if c.ShowLinks && first(host) {
		return c.LANIP
	}
	return g.IP
}
func Controlled(c config.Config, host string) bool {
	return GroupFor(host) != "" && (Address(c, host) != "" || c.Groups[GroupFor(host)].IP != "")
}
func Redirect(c config.Config, host string) string {
	host = Normalize(host)
	key := GroupFor(host)
	if key == "" || key == "store" {
		return ""
	}
	legacy := strings.HasPrefix(host, "dlassets")
	if key == "global" {
		suffix := ".com"
		if c.RedirectToCN && c.Groups["global"].IP == "" {
			suffix = ".cn"
		}
		stem := "assets1"
		if legacy {
			stem = "dlassets"
		}
		if c.ShowLinks || !first(host) {
			stem = "assets2"
			if legacy {
				stem = "dlassets2"
			}
		}
		dest := stem + ".xboxlive" + suffix
		if dest == host {
			return ""
		}
		return dest
	}
	if c.ShowLinks && first(host) {
		if legacy {
			return "dlassets2.xboxlive.cn"
		}
		return "assets2.xboxlive.cn"
	}
	return ""
}
