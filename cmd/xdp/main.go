package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"xboxdownloadproxy/internal/app"
	"xboxdownloadproxy/internal/config"
)

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "xdp:", e)
		os.Exit(1)
	}
}
func usage() {
	fmt.Println(`Xbox Download Proxy 0.1.0
Usage: xdp <command> [flags]
Commands:
  init       Create config.json and management token
  serve      Run WebUI/API (use --start to also start download services)
  status     Show service status
  start      Start DNS/HTTP/TLS download services
  stop       Stop download services; management API stays available
  config     Get config; --file FILE replaces it while stopped
  sources    Load candidates (--group cn1 [--refresh])
  import     Import candidate IP text (--group cn1 --file FILE)
  speedtest  Test candidates (--group cn1 --ips IP,IP [--test-url URL] [--wait])
  apply      Save chosen CDN IP (--group cn1 --ip IP); stops download services
  logs       Show recent requests (--after ID)
  download   Add an offline download (--source URL [--name NAME])
  downloads  List offline tasks and disk space
  download-pause/resume/cancel/delete --id ID
  download-save --id ID --output FILE (save a completed file locally)
  doctor     Check configuration and available local bind ports
Flags follow the command. Use xdp <command> --help for options.
Data defaults to the OS user configuration directory / xboxdownloadproxy.`)
}
func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		usage()
		return nil
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Println("0.1.0")
		return nil
	}
	command := args[0]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	dir := f.String("data-dir", config.DefaultDir(), "configuration and cache directory")
	base := f.String("url", "", "management API URL (default local configured web port)")
	token := f.String("token", os.Getenv("XDP_TOKEN"), "management token (or XDP_TOKEN; default data-dir/token)")
	group := f.String("group", "cn1", "global, cn1, cn2 or store")
	file := f.String("file", "", "input file")
	ips := f.String("ips", "", "comma-separated public IPv4 candidates")
	ip := f.String("ip", "", "chosen CDN IPv4")
	target := f.String("test-url", "", "real download URL (overrides group testURL)")
	refresh := f.Bool("refresh", false, "refresh online candidate source")
	wait := f.Bool("wait", false, "wait for speed test completion")
	after := f.Uint64("after", 0, "log cursor")
	lan := f.String("lan-ip", "", "server LAN IPv4")
	web := f.String("web", "", "WebUI bind address")
	dns := f.String("dns", "", "DNS bind address")
	httpAddr := f.String("http", "", "HTTP redirect bind address")
	tls := f.String("tls", "", "TLS bind address (empty disables if explicitly specified)")
	upstream := f.String("upstream", "", "upstream DNS IP:port")
	start := f.Bool("start", false, "start download services with management server")
	downloadDir := f.String("download-dir", "", "serve: download directory (default data-dir/downloads)")
	offlineEnabled := f.Bool("offline", false, "init/serve: enable optional offline downloads and Xbox HTTP local delivery")
	source := f.String("source", "", "offline download HTTP(S) URL")
	name := f.String("name", "", "optional offline download filename")
	id := f.String("id", "", "offline task ID")
	output := f.String("output", "", "local destination for download-save; must not exist")
	if e := f.Parse(args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if len(f.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	switch command {
	case "init", "serve", "doctor", "status", "start", "stop", "config", "sources", "import", "speedtest", "apply", "logs", "download", "downloads", "download-pause", "download-resume", "download-cancel", "download-delete", "download-save":
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	c, e := config.Load(*dir)
	if errors.Is(e, os.ErrNotExist) {
		c = config.Default()
		if command == "init" || command == "serve" {
			if e = config.Save(*dir, c); e != nil {
				return e
			}
		}
	} else if e != nil {
		return e
	}
	if command == "init" {
		f.Visit(func(fl *flag.Flag) {
			if fl.Name == "offline" {
				c.OfflineEnabled = *offlineEnabled
			}
		})
		if e := config.Save(*dir, c); e != nil {
			return e
		}
		managementToken, err := config.Token(*dir)
		if err != nil {
			return err
		}
		fmt.Println("Configuration:", filepath.Join(*dir, "config.json"))
		fmt.Println("Token file:", filepath.Join(*dir, "token"))
		fmt.Println("Management token:", managementToken)
		fmt.Printf("Offline downloads and Xbox HTTP local delivery: %t\n", c.OfflineEnabled)
		fmt.Println("Download services are OFF by default. Start them in WebUI after setup.")
		return nil
	}
	if command == "doctor" {
		return doctor(c)
	}
	if command == "serve" {
		f.Visit(func(fl *flag.Flag) {
			switch fl.Name {
			case "lan-ip":
				c.LANIP = *lan
			case "web":
				c.WebListen = *web
			case "dns":
				c.DNSListen = *dns
			case "http":
				c.HTTPListen = *httpAddr
			case "tls":
				c.TLSListen = *tls
			case "upstream":
				c.UpstreamDNS = *upstream
			case "offline":
				c.OfflineEnabled = *offlineEnabled
			}
		})
		if e = config.Save(*dir, c); e != nil {
			return e
		}
		if *token == "" {
			*token, e = config.Token(*dir)
			if e != nil {
				return e
			}
		}
		return serve(*dir, *token, c, *start, *downloadDir)
	}
	if *token == "" {
		b, e := os.ReadFile(filepath.Join(*dir, "token"))
		if e != nil {
			return fmt.Errorf("read management token: %w (or pass --token)", e)
		}
		*token = strings.TrimSpace(string(b))
	}
	if *base == "" {
		host, port, e := net.SplitHostPort(c.WebListen)
		if e != nil {
			return e
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		*base = "http://" + net.JoinHostPort(host, port)
	}
	var path, method string
	method = "GET"
	var body any
	switch command {
	case "status":
		path = "/api/status"
	case "start", "stop":
		path = "/api/service/" + command
		method = "POST"
		body = map[string]any{}
	case "config":
		path = "/api/config"
		if *file != "" {
			b, e := os.ReadFile(*file)
			if e != nil {
				return e
			}
			var next config.Config
			if e = json.Unmarshal(b, &next); e != nil {
				return e
			}
			method = "PUT"
			body = next
		}
	case "sources":
		path = "/api/candidates?group=" + url.QueryEscape(*group) + "&refresh=" + strconv.FormatBool(*refresh)
	case "import":
		if *file == "" {
			return errors.New("--file is required")
		}
		b, e := os.ReadFile(*file)
		if e != nil {
			return e
		}
		method = "POST"
		path = "/api/candidates/import"
		body = map[string]any{"group": *group, "text": string(b)}
	case "speedtest":
		method = "POST"
		path = "/api/tests"
		list := []string{}
		for _, s := range strings.Split(*ips, ",") {
			if v := strings.TrimSpace(s); v != "" {
				list = append(list, v)
			}
		}
		body = map[string]any{"group": *group, "ips": list, "testURL": *target}
	case "apply":
		method = "POST"
		path = "/api/apply"
		body = map[string]any{"group": *group, "ip": *ip}
	case "logs":
		path = "/api/logs?after=" + strconv.FormatUint(*after, 10)
	case "download":
		if *source == "" {
			return errors.New("--source URL is required")
		}
		method, path = "POST", "/api/downloads"
		body = map[string]string{"url": *source, "name": *name}
	case "downloads":
		path = "/api/downloads"
	case "download-pause", "download-resume", "download-cancel", "download-delete", "download-save":
		if len(*id) != 32 {
			return errors.New("--id must be a 32-character task ID")
		}
		if _, err := strconv.ParseUint((*id)[:16], 16, 64); err != nil {
			return errors.New("invalid task ID")
		}
		if _, err := strconv.ParseUint((*id)[16:], 16, 64); err != nil {
			return errors.New("invalid task ID")
		}
		path = "/api/downloads/" + *id
		if command == "download-save" {
			return saveDownload(*base, *token, path+"/file", *output)
		}
		if command == "download-delete" {
			method = "DELETE"
		} else {
			method = "POST"
			path += "/" + strings.TrimPrefix(command, "download-")
		}
	}
	b, e := request(*base, *token, method, path, body)
	if e != nil {
		return e
	}
	if command == "download-delete" {
		fmt.Println("Task and downloaded file deleted.")
		return nil
	}
	if command == "speedtest" && *wait {
		var job struct{ ID, State string }
		if e = json.Unmarshal(b, &job); e != nil {
			return e
		}
		for job.State == "running" {
			time.Sleep(time.Second)
			b, e = request(*base, *token, "GET", "/api/tests/"+job.ID, nil)
			if e != nil {
				return e
			}
			if e = json.Unmarshal(b, &job); e != nil {
				return e
			}
		}
	}
	var pretty bytes.Buffer
	if e = json.Indent(&pretty, b, "", "  "); e != nil {
		return e
	}
	fmt.Println(pretty.String())
	return nil
}
func request(base, token, method, path string, body any) ([]byte, error) {
	var data []byte
	var e error
	if body != nil {
		data, e = json.Marshal(body)
		if e != nil {
			return nil, e
		}
	}
	req, e := http.NewRequest(method, strings.TrimSuffix(base, "/")+path, bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 32*1024*1024))
	if e != nil {
		return nil, e
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("API %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	return b, nil
}
func saveDownload(base, token, path, output string) error {
	if output == "" {
		return errors.New("--output FILE is required")
	}
	req, err := http.NewRequest("GET", strings.TrimSuffix(base, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Second
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("API %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(output)
		}
	}()
	n, err := io.Copy(f, res.Body)
	if err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	fmt.Printf("Saved %d bytes to %s\n", n, output)
	return nil
}

func serve(dir, token string, c config.Config, start bool, downloadDir string) error {
	ln, e := net.Listen("tcp", c.WebListen)
	if e != nil {
		return fmt.Errorf("management listener %s: %w", c.WebListen, e)
	}
	a := app.NewWithDownloads(dir, token, c, downloadDir)
	defer a.Close()
	if err := a.DownloadError(); err != nil {
		ln.Close()
		return fmt.Errorf("download directory: %w", err)
	}
	server := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	defer ln.Close()
	if start || c.AutoStart {
		if e := a.Start(); e != nil {
			return e
		}
	}
	fmt.Printf("Xbox Download Proxy 0.1.0\nWebUI: http://%s%s\nToken: %s\nData: %s\n", c.LANIP, webPort(c.WebListen), token, dir)
	if a.Status().Running {
		fmt.Println("Download services: ON (DNS / HTTP / configured TLS)")
	} else {
		fmt.Println("Download services: OFF — open WebUI, finish setup, then start the service before changing client DNS.")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	case e := <-errs:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
		return nil
	}
}
func webPort(addr string) string { _, p, _ := net.SplitHostPort(addr); return ":" + p }
func doctor(c config.Config) error {
	if e := config.Validate(c); e != nil {
		return e
	}
	failed := false
	for _, item := range []struct{ name, network, addr string }{{"DNS UDP", "udp", c.DNSListen}, {"DNS TCP", "tcp", c.DNSListen}, {"HTTP", "tcp", c.HTTPListen}, {"TLS", "tcp", c.TLSListen}, {"WebUI", "tcp", c.WebListen}} {
		if item.addr == "" {
			continue
		}
		var e error
		if item.network == "udp" {
			var conn net.PacketConn
			conn, e = net.ListenPacket("udp", item.addr)
			if conn != nil {
				conn.Close()
			}
		} else {
			var conn net.Listener
			conn, e = net.Listen("tcp", item.addr)
			if conn != nil {
				conn.Close()
			}
		}
		if e != nil {
			failed = true
			fmt.Printf("FAIL %s %s: %v\n", item.name, item.addr, e)
		} else {
			fmt.Printf("OK   %s %s\n", item.name, item.addr)
		}
	}
	fmt.Println("LAN IP:", c.LANIP, "(must be reachable by clients)")
	fmt.Println("No host DNS, hosts, firewall or router settings were changed.")
	if failed {
		return errors.New("one or more ports unavailable; use an unused address/port and check privileges")
	}
	return nil
}
