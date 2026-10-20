package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xboxdownloadproxy/internal/config"
)

func writeCLIState(t *testing.T, dir string) {
	t.Helper()
	if err := config.Save(dir, config.Default()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHelpVersionAndFlagErrorsDoNotRequireInitializedState(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no arguments", nil, "Usage: xdp <command>"},
		{"help command", []string{"help"}, "Usage: xdp <command>"},
		{"help flag", []string{"--help"}, "Usage: xdp <command>"},
		{"version command", []string{"version"}, "0.0.3"},
		{"version flag", []string{"--version"}, "0.0.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := captureCommandOutput(t, func() error { return run(tc.args) })
			if err != nil || !strings.Contains(output, tc.want) {
				t.Fatalf("run(%v) = %q, %v", tc.args, output, err)
			}
		})
	}
	if err := run([]string{"status", "--help"}); err != nil {
		t.Fatal("command help requires configuration:", err)
	}
	if err := run([]string{"status", "--unknown-flag"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err := run([]string{"status", "--data-dir", t.TempDir(), "--token=", "--url", "http://127.0.0.1:1"}); err == nil || !strings.Contains(err.Error(), "read management token") {
		t.Fatalf("missing token error = %v", err)
	}
}

func TestRunCommandsSendExpectedManagementRequests(t *testing.T) {
	id := strings.Repeat("a", 32)
	cases := []struct {
		name               string
		args               func(string, string) []string
		method, path, body string
	}{
		{"status", func(dir, base string) []string {
			return []string{"status", "--data-dir", dir, "--url", base + "/", "--token", "secret"}
		}, "GET", "/api/status", ""},
		{"config get", func(dir, base string) []string {
			return []string{"config", "--data-dir", dir, "--url", base, "--token", "secret"}
		}, "GET", "/api/config", ""},
		{"config replace", func(dir, base string) []string {
			p := filepath.Join(dir, "replacement.json")
			b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
			_ = os.WriteFile(p, b, 0600)
			return []string{"config", "--data-dir", dir, "--url", base, "--token", "secret", "--file", p}
		}, "PUT", "/api/config", `"schemaVersion":1`},
		{"start", func(dir, base string) []string {
			return []string{"start", "--data-dir", dir, "--url", base, "--token", "secret"}
		}, "POST", "/api/service/start", "{}"},
		{"stop", func(dir, base string) []string {
			return []string{"stop", "--data-dir", dir, "--url", base, "--token", "secret"}
		}, "POST", "/api/service/stop", "{}"},
		{"sources query", func(dir, base string) []string {
			return []string{"sources", "--data-dir", dir, "--url", base, "--token", "secret", "--group", "cn1 & cn2", "--refresh"}
		}, "GET", "/api/candidates?group=cn1+%26+cn2&refresh=true", ""},
		{"import", func(dir, base string) []string {
			p := filepath.Join(dir, "candidates.txt")
			_ = os.WriteFile(p, []byte("1.1.1.1 edge\n"), 0600)
			return []string{"import", "--data-dir", dir, "--url", base, "--token", "secret", "--group", "cn1", "--file", p}
		}, "POST", "/api/candidates/import", `"group":"cn1","text":"1.1.1.1 edge\n"`},
		{"speedtest", func(dir, base string) []string {
			return []string{"speedtest", "--data-dir", dir, "--url", base, "--token", "secret", "--group", "cn2", "--ips", "1.1.1.1, 8.8.8.8", "--test-url", "https://example.com/test"}
		}, "POST", "/api/tests", `"group":"cn2","ips":["1.1.1.1","8.8.8.8"],"testURL":"https://example.com/test"`},
		{"apply", func(dir, base string) []string {
			return []string{"apply", "--data-dir", dir, "--url", base, "--token", "secret", "--group", "cn1", "--ip", "1.1.1.1"}
		}, "POST", "/api/apply", `"group":"cn1","ip":"1.1.1.1"`},
		{"logs", func(dir, base string) []string {
			return []string{"logs", "--data-dir", dir, "--url", base, "--token", "secret", "--after", "42"}
		}, "GET", "/api/logs?after=42", ""},
		{"download", func(dir, base string) []string {
			return []string{"download", "--data-dir", dir, "--url", base, "--token", "secret", "--source", "https://example.com/game", "--name", "game.bin"}
		}, "POST", "/api/downloads", `"url":"https://example.com/game"
"name":"game.bin"`},
		{"downloads", func(dir, base string) []string {
			return []string{"downloads", "--data-dir", dir, "--url", base, "--token", "secret"}
		}, "GET", "/api/downloads", ""},
		{"download pause", func(dir, base string) []string {
			return []string{"download-pause", "--data-dir", dir, "--url", base, "--token", "secret", "--id", id}
		}, "POST", "/api/downloads/" + id + "/pause", ""},
		{"download resume", func(dir, base string) []string {
			return []string{"download-resume", "--data-dir", dir, "--url", base, "--token", "secret", "--id", id}
		}, "POST", "/api/downloads/" + id + "/resume", ""},
		{"download cancel", func(dir, base string) []string {
			return []string{"download-cancel", "--data-dir", dir, "--url", base, "--token", "secret", "--id", id}
		}, "POST", "/api/downloads/" + id + "/cancel", ""},
		{"download delete", func(dir, base string) []string {
			return []string{"download-delete", "--data-dir", dir, "--url", base, "--token", "secret", "--id", id}
		}, "DELETE", "/api/downloads/" + id, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCLIState(t, dir)
			seen := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = true
				if r.Method != tc.method || r.URL.RequestURI() != tc.path || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("request = %s %s auth=%q content-type=%q", r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("Content-Type"))
				}
				b := new(bytes.Buffer)
				_, _ = b.ReadFrom(r.Body)
				if tc.body != "" {
					for _, expected := range strings.Split(tc.body, "\n") {
						if !strings.Contains(b.String(), expected) {
							t.Errorf("request body %q does not contain %q", b.String(), expected)
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			if err := run(tc.args(dir, srv.URL)); err != nil {
				t.Fatal(err)
			}
			if !seen {
				t.Fatal("command made no management request")
			}
		})
	}
}

func TestRunSpeedtestWaitPollsTheReturnedJob(t *testing.T) {
	dir := t.TempDir()
	writeCLIState(t, dir)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1:
			if r.Method != http.MethodPost || r.URL.Path != "/api/tests" {
				t.Errorf("initial request = %s %s", r.Method, r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"job-1","state":"running"}`))
		case 2:
			if r.Method != http.MethodGet || r.URL.Path != "/api/tests/job-1" {
				t.Errorf("poll request = %s %s", r.Method, r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"job-1","state":"done"}`))
		default:
			t.Errorf("unexpected request %d", requests)
		}
	}))
	defer srv.Close()
	if err := run([]string{"speedtest", "--data-dir", dir, "--url", srv.URL, "--token", "secret", "--ips", "1.1.1.1", "--test-url", "https://example.com/file", "--wait"}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want initial POST and one poll", requests)
	}
}

func TestRequestRejectsRedirectsAndReturnsAPIErrorBody(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect target was contacted") }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/should-not-follow", http.StatusFound)
	}))
	defer redirect.Close()
	if _, err := request(redirect.URL, "secret", http.MethodGet, "/api/status", nil); err == nil || !strings.Contains(err.Error(), "API 302 Found") {
		t.Fatalf("redirect response error = %v", err)
	}
	failure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"service busy"}`))
	}))
	defer failure.Close()
	if _, err := request(failure.URL, "secret", http.MethodPost, "/api/service/start", map[string]any{}); err == nil || !strings.Contains(err.Error(), `API 409 Conflict: {"error":"service busy"}`) {
		t.Fatalf("API error response = %v", err)
	}
	if _, err := request("http://[::1", "secret", http.MethodGet, "/api/status", nil); err == nil {
		t.Fatal("invalid base URL succeeded")
	}
}

func TestRunRejectsInvalidCommandParameters(t *testing.T) {
	dir := t.TempDir()
	writeCLIState(t, dir)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"no-such-command"}, `unknown command "no-such-command"`},
		{"unexpected positional argument", []string{"status", "--data-dir", dir, "extra"}, "unexpected positional arguments"},
		{"download needs source", []string{"download", "--data-dir", dir, "--token", "secret"}, "--source URL is required"},
		{"task id length", []string{"download-pause", "--data-dir", dir, "--token", "secret", "--id", "abc"}, "--id must be a 32-character task ID"},
		{"task id hex", []string{"download-resume", "--data-dir", dir, "--token", "secret", "--id", strings.Repeat("g", 32)}, "invalid task ID"},
		{"download save output", []string{"download-save", "--data-dir", dir, "--token", "secret", "--id", strings.Repeat("a", 32)}, "--output FILE is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.args
			if len(args) > 0 && args[0] != "no-such-command" {
				// Avoid dependence on the user's default management port.
				args = append(args, "--url", "http://127.0.0.1:1")
			}
			err := run(args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run(%v) error = %v, want substring %q", args, err, tc.want)
			}
		})
	}
}

func TestSaveDownloadRemovesPartialFileAfterDisconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial payload"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()
	output := filepath.Join(t.TempDir(), "interrupted.bin")
	err := saveDownload(srv.URL, "secret", "/file", output)
	if err == nil {
		t.Fatal("truncated response succeeded")
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("partial file remains after disconnect: stat err=%v", statErr)
	}
}

func freeHighPort(t *testing.T, network string) string {
	t.Helper()
	if network == "udp" {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().String()
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestDoctorChecksHighLocalPorts(t *testing.T) {
	dns := freeHighPort(t, "tcp")
	c := config.Default()
	c.LANIP = "127.0.0.1"
	c.DNSListen = dns
	c.HTTPListen = freeHighPort(t, "tcp")
	c.TLSListen = freeHighPort(t, "tcp")
	c.WebListen = freeHighPort(t, "tcp")
	if err := doctor(c); err != nil {
		t.Fatalf("doctor rejected free high local ports: %v", err)
	}
}

func captureCommandOutput(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = w
	callErr := fn()
	_ = w.Close()
	os.Stdout = previous
	output, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(output), callErr
}

func assertTCPListenerStillAccepts(t *testing.T, listener net.Listener) {
	t.Helper()
	client, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("original listener no longer accepts connections: %v", err)
	}
	defer client.Close()
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept on original listener: %v", err)
	}
	_ = accepted.Close()
}

func assertUDPListenerStillReceives(t *testing.T, conn net.PacketConn) {
	t.Helper()
	payload := []byte("still-bound")
	if _, err := conn.WriteTo(payload, conn.LocalAddr()); err != nil {
		t.Fatalf("write to original UDP listener: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(payload))
	n, _, err := conn.ReadFrom(buf)
	if err != nil || string(buf[:n]) != string(payload) {
		t.Fatalf("original UDP listener did not receive its packet: %q %v", buf[:n], err)
	}
}

func TestDoctorReportsEachOccupiedPortWithoutDisruptingListeners(t *testing.T) {
	dnsTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dnsTCP.Close()
	dnsUDP, err := net.ListenPacket("udp", dnsTCP.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer dnsUDP.Close()
	occupied := map[string]net.Listener{"DNS TCP": dnsTCP}
	for _, name := range []string{"HTTP", "TLS", "WebUI"} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		occupied[name] = listener
	}
	c := config.Default()
	c.LANIP = "127.0.0.1"
	c.DNSListen = dnsTCP.Addr().String()
	c.HTTPListen = occupied["HTTP"].Addr().String()
	c.TLSListen = occupied["TLS"].Addr().String()
	c.WebListen = occupied["WebUI"].Addr().String()
	output, err := captureCommandOutput(t, func() error { return doctor(c) })
	if err == nil || !strings.Contains(err.Error(), "one or more ports unavailable") {
		t.Fatalf("doctor error = %v, output=%s", err, output)
	}
	for _, name := range []string{"DNS UDP", "DNS TCP", "HTTP", "TLS", "WebUI"} {
		if !strings.Contains(output, "FAIL "+name+" ") {
			t.Errorf("doctor did not report %s as occupied: %s", name, output)
		}
	}
	assertUDPListenerStillReceives(t, dnsUDP)
	for _, listener := range occupied {
		assertTCPListenerStillAccepts(t, listener)
	}
}

func TestServeOccupiedWebPortReturnsUsefulError(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	c := config.Default()
	c.LANIP = "127.0.0.1"
	c.WebListen = occupied.Addr().String()
	err = serve(t.TempDir(), "secret", c, false, "")
	if err == nil || !strings.Contains(err.Error(), "management listener "+occupied.Addr().String()) {
		t.Fatalf("serve error = %v", err)
	}
	assertTCPListenerStillAccepts(t, occupied)
}

func TestRunServePersistsExplicitBindOverridesBeforeWebBindFailure(t *testing.T) {
	dir := t.TempDir()
	c := config.Default()
	c.OfflineEnabled = true
	if err := config.Save(dir, c); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	occupiedWeb, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupiedWeb.Close()
	newDNS, newHTTP := freeHighPort(t, "tcp"), freeHighPort(t, "tcp")
	args := []string{
		"serve", "--data-dir", dir, "--token", "secret",
		"--lan-ip", "127.0.0.2", "--web", occupiedWeb.Addr().String(),
		"--dns", newDNS, "--http", newHTTP, "--tls=",
		"--upstream", "127.0.0.1:5353", "--offline=false",
	}
	err = run(args)
	if err == nil || !strings.Contains(err.Error(), "management listener "+occupiedWeb.Addr().String()) {
		t.Fatalf("serve error = %v", err)
	}
	saved, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.LANIP != "127.0.0.2" || saved.WebListen != occupiedWeb.Addr().String() || saved.DNSListen != newDNS || saved.HTTPListen != newHTTP || saved.TLSListen != "" || saved.UpstreamDNS != "127.0.0.1:5353" || saved.OfflineEnabled {
		t.Fatalf("explicit serve flags were not persisted: %+v", saved)
	}
	assertTCPListenerStillAccepts(t, occupiedWeb)
}

func TestServeOfflineLibraryFailureReleasesWebListener(t *testing.T) {
	dir := t.TempDir()
	blockedPath := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blockedPath, []byte("block child directory"), 0600); err != nil {
		t.Fatal(err)
	}
	web := freeHighPort(t, "tcp")
	c := config.Default()
	c.LANIP = "127.0.0.1"
	c.WebListen = web
	c.OfflineEnabled = true
	err := serve(filepath.Join(dir, "data"), "secret", c, false, filepath.Join(blockedPath, "downloads"))
	if err == nil || !strings.Contains(err.Error(), "download directory") {
		t.Fatalf("serve error = %v", err)
	}
	listener, err := net.Listen("tcp", web)
	if err != nil {
		t.Fatalf("Web listener remained bound after offline initialization failed: %v", err)
	}
	_ = listener.Close()
}

func TestServeStartFailureReleasesWebAndPartiallyOpenedDNS(t *testing.T) {
	blockedDNSTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blockedDNSTCP.Close()
	dns := blockedDNSTCP.Addr().String()
	web := freeHighPort(t, "tcp")
	c := config.Default()
	c.LANIP = "127.0.0.1"
	c.DNSListen = dns
	c.HTTPListen = freeHighPort(t, "tcp")
	c.TLSListen = ""
	c.WebListen = web
	err = serve(t.TempDir(), "secret", c, true, "")
	if err == nil || !strings.Contains(err.Error(), "DNS TCP "+dns) {
		t.Fatalf("serve start error = %v", err)
	}
	udp, err := net.ListenPacket("udp", dns)
	if err != nil {
		t.Fatalf("partially opened DNS UDP listener was not released: %v", err)
	}
	_ = udp.Close()
	listener, err := net.Listen("tcp", web)
	if err != nil {
		t.Fatalf("Web listener remained bound after failed start: %v", err)
	}
	_ = listener.Close()
	assertTCPListenerStillAccepts(t, blockedDNSTCP)
}
