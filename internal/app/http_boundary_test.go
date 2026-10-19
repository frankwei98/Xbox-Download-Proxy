package app

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xboxdownloadproxy/internal/config"
)

func apiCall(a *App, method, path, body, auth, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	r.Host = "localhost"
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func assertProblem(t *testing.T, w *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var got map[string]string
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, status, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got["error"] != message {
		t.Fatalf("problem body = %s, want error %q (decode err %v)", w.Body.String(), message, err)
	}
}

func TestManagementAuthenticationAndOriginBoundaries(t *testing.T) {
	a := New(t.TempDir(), "secret", config.Default())
	defer a.Close()
	for _, auth := range []string{"", "secret", "Basic secret", "Bearer", "Bearer ", "Bearer wrong", "Bearer secret ", "bearer secret"} {
		w := apiCall(a, http.MethodGet, "/api/status", "", auth, "")
		assertProblem(t, w, http.StatusUnauthorized, "management token required")
	}
	for _, origin := range []string{"http://evil.test", "https://localhost", "ftp://localhost", "http://localhost:80", "not a URL"} {
		w := apiCall(a, http.MethodGet, "/api/status", "", "Bearer secret", origin)
		assertProblem(t, w, http.StatusForbidden, "cross-origin management request rejected")
	}
	for _, origin := range []string{"", "http://localhost"} {
		w := apiCall(a, http.MethodGet, "/api/status", "", "Bearer secret", origin)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("same-origin status request: %d %s headers=%v", w.Code, w.Body.String(), w.Header())
		}
	}
	secureRequest := httptest.NewRequest(http.MethodGet, "https://localhost/api/status", nil)
	secureRequest.TLS = &tls.ConnectionState{}
	secureRequest.Header.Set("Authorization", "Bearer secret")
	secureRequest.Header.Set("Origin", "https://localhost")
	secureResponse := httptest.NewRecorder()
	a.Handler().ServeHTTP(secureResponse, secureRequest)
	if secureResponse.Code != http.StatusOK {
		t.Fatalf("same-origin HTTPS request rejected: %d %s", secureResponse.Code, secureResponse.Body.String())
	}
	emptyToken := New(t.TempDir(), "", config.Default())
	defer emptyToken.Close()
	w := apiCall(emptyToken, http.MethodGet, "/api/status", "", "Bearer ", "")
	assertProblem(t, w, http.StatusUnauthorized, "management token required")
}

func TestManagementJSONValidationAndRouteMisses(t *testing.T) {
	a := New(t.TempDir(), "secret", config.Default())
	defer a.Close()
	valid, err := json.Marshal(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(valid, []byte(`"schemaVersion"`), []byte(`"unknownField":true,"schemaVersion"`), 1)
	for name, body := range map[string]string{
		"malformed":      `{`,
		"unknown field":  string(unknown),
		"multiple value": string(valid) + ` {}`,
		"oversized":      `{"padding":"` + strings.Repeat("x", 2*1024*1024) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := apiCall(a, http.MethodPut, "/api/config", body, "Bearer secret", "")
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
			var got map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got["error"] == "" {
				t.Fatalf("missing JSON error response: %s (%v)", w.Body.String(), err)
			}
		})
	}
	if w := apiCall(a, http.MethodPost, "/api/no-such-route", `{}`, "Bearer secret", ""); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "API endpoint not found") {
		t.Fatalf("unknown route = %d %s", w.Code, w.Body.String())
	}
	if w := apiCall(a, http.MethodPost, "/api/status", `{}`, "Bearer secret", ""); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "API endpoint not found") {
		t.Fatalf("wrong method = %d %s, want API 404", w.Code, w.Body.String())
	}
}

func TestManagementReadAndCandidateRoutes(t *testing.T) {
	dir := t.TempDir()
	c := config.Default()
	c.Groups["cn1"] = config.Group{SourceURL: "", TestURL: ""}
	a := New(dir, "secret", c)
	defer a.Close()
	if w := apiCall(a, http.MethodGet, "/api/status", "", "Bearer secret", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"running":false`) {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	if w := apiCall(a, http.MethodGet, "/api/config", "", "Bearer secret", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"webListen":":8080"`) {
		t.Fatalf("config: %d %s", w.Code, w.Body.String())
	}
	w := apiCall(a, http.MethodGet, "/api/groups", "", "Bearer secret", "")
	var groups []struct {
		ID        string `json:"id"`
		TestURL   string `json:"testURL"`
		SourceURL string `json:"sourceURL"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &groups); err != nil || w.Code != 200 || len(groups) != 4 {
		t.Fatalf("groups: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	found := false
	for _, group := range groups {
		if group.ID == "cn1" {
			found = true
			if group.TestURL != "" || group.SourceURL != "" {
				t.Fatalf("group response did not reflect config: %+v", group)
			}
		}
	}
	if !found {
		t.Fatalf("cn1 absent from group response: %+v", groups)
	}
	w = apiCall(a, http.MethodPost, "/api/candidates/import", `{"group":"cn1","text":"1.1.1.1 edge\n8.8.8.8\n"}`, "Bearer secret", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"origin":"import"`) || !strings.Contains(w.Body.String(), `"location":"edge"`) {
		t.Fatalf("candidate import: %d %s", w.Code, w.Body.String())
	}
	w = apiCall(a, http.MethodGet, "/api/candidates?group=cn1", "", "Bearer secret", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"origin":"cache"`) || !strings.Contains(w.Body.String(), `1.1.1.1`) {
		t.Fatalf("candidate cache: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ body, message string }{
		{`{`, "error"},
		{`{"group":"absent","text":"1.1.1.1"}`, "unknown group"},
		{`{"group":"cn1","text":"not-an-ip"}`, "list contains no public IPv4"},
	} {
		w := apiCall(a, http.MethodPost, "/api/candidates/import", tc.body, "Bearer secret", "")
		if w.Code != 400 || !strings.Contains(w.Body.String(), tc.message) {
			t.Fatalf("candidate import error: %d %s", w.Code, w.Body.String())
		}
	}
	w = apiCall(a, http.MethodGet, "/api/candidates?group=absent", "", "Bearer secret", "")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "unknown group") {
		t.Fatalf("candidate lookup error: %d %s", w.Code, w.Body.String())
	}
}

func TestManagementConfigUpdatePersistsAndReturnsConfig(t *testing.T) {
	dir := t.TempDir()
	a := New(dir, "secret", config.Default())
	defer a.Close()
	next := a.Config()
	next.ShowLinks = false
	next.Groups["cn1"] = config.Group{IP: "1.1.1.1", SourceURL: "https://candidates.example/cn1", TestURL: "https://test.example/file"}
	body, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	w := apiCall(a, http.MethodPut, "/api/config", string(body), "Bearer secret", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"showLinks":false`) || !strings.Contains(w.Body.String(), `"ip":"1.1.1.1"`) {
		t.Fatalf("config update response: %d %s", w.Code, w.Body.String())
	}
	saved, err := config.Load(dir)
	if err != nil || saved.ShowLinks || saved.Groups["cn1"].IP != "1.1.1.1" {
		t.Fatalf("saved config = %+v, err=%v", saved, err)
	}
}

func TestManagementApplyAndTestRoutes(t *testing.T) {
	c := config.Default()
	a := New(t.TempDir(), "secret", c)
	defer a.Close()
	for _, tc := range []struct{ body, text string }{
		{`{`, "error"},
		{`{"group":"absent","ip":"1.1.1.1"}`, "unknown group"},
		{`{"group":"cn1","ip":"127.0.0.1"}`, "public CDN IPv4"},
	} {
		w := apiCall(a, http.MethodPost, "/api/apply", tc.body, "Bearer secret", "")
		if w.Code != 400 || !strings.Contains(w.Body.String(), tc.text) {
			t.Fatalf("apply error: %d %s", w.Code, w.Body.String())
		}
	}
	w := apiCall(a, http.MethodPost, "/api/apply", `{"group":"cn1","ip":"1.1.1.1"}`, "Bearer secret", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"running":false`) || a.Config().Groups["cn1"].IP != "1.1.1.1" {
		t.Fatalf("apply success: %d %s config=%+v", w.Code, w.Body.String(), a.Config().Groups["cn1"])
	}
	for _, tc := range []struct{ body, text string }{
		{`{`, "error"},
		{`{"group":"absent","ips":["1.1.1.1"],"testURL":"https://example.com/a"}`, "unknown group"},
		{`{"group":"store","ips":["1.1.1.1"]}`, "needs a real test download URL"},
		{`{"group":"cn1","ips":[],"testURL":"https://example.com/a"}`, "select between 1 and 128"},
	} {
		w := apiCall(a, http.MethodPost, "/api/tests", tc.body, "Bearer secret", "")
		if w.Code != 400 || !strings.Contains(w.Body.String(), tc.text) {
			t.Fatalf("test start error: %d %s", w.Code, w.Body.String())
		}
	}
	// Exercise scheduling without ever dialing a public candidate. A zero test
	// budget expires the probe's context before the HTTP transport can connect.
	probeConfig := a.Config()
	probeConfig.Test.TimeoutSeconds = 0
	a = New(t.TempDir(), "secret", probeConfig)
	defer a.Close()
	w = apiCall(a, http.MethodPost, "/api/tests", `{"group":"cn1","ips":["1.1.1.1","1.1.1.1"],"testURL":"http://127.0.0.1/test"}`, "Bearer secret", "")
	if w.Code != 202 {
		t.Fatalf("test start: %d %s", w.Code, w.Body.String())
	}
	var job struct {
		ID    string `json:"id"`
		Total int    `json:"total"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || job.ID == "" || job.Total != 1 || job.State != "running" {
		t.Fatalf("test job response: %+v %v (%s)", job, err, w.Body.String())
	}
	w = apiCall(a, http.MethodGet, "/api/tests/absent", "", "Bearer secret", "")
	assertProblem(t, w, http.StatusNotFound, "test not found")
	w = apiCall(a, http.MethodPost, "/api/tests/"+job.ID+"/cancel", `{}`, "Bearer secret", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"`+job.ID+`"`) {
		t.Fatalf("test cancel: %d %s", w.Code, w.Body.String())
	}
	w = apiCall(a, http.MethodGet, "/api/tests/"+job.ID, "", "Bearer secret", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"`+job.ID+`"`) {
		t.Fatalf("test lookup: %d %s", w.Code, w.Body.String())
	}
	w = apiCall(a, http.MethodPost, "/api/tests/absent/cancel", `{}`, "Bearer secret", "")
	assertProblem(t, w, http.StatusNotFound, "test not found")
}

func TestManagementServiceActionResponses(t *testing.T) {
	c := config.Default()
	c.LANIP = "127.0.0.1"
	c.DNSListen, c.HTTPListen, c.WebListen = unused(t), unused(t), unused(t)
	c.TLSListen = ""
	a := New(t.TempDir(), "secret", c)
	defer a.Close()
	w := apiCall(a, http.MethodPost, "/api/service/start", `{}`, "Bearer secret", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"running":true`) {
		t.Fatalf("start response: %d %s", w.Code, w.Body.String())
	}
	w = apiCall(a, http.MethodPost, "/api/service/stop", `{}`, "Bearer secret", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"running":false`) {
		t.Fatalf("stop response: %d %s", w.Code, w.Body.String())
	}
	if a.Status().Running {
		t.Fatal("service remains running after stop response")
	}
	invalid := config.Default()
	invalid.SchemaVersion = 2
	b := New(t.TempDir(), "secret", invalid)
	defer b.Close()
	w = apiCall(b, http.MethodPost, "/api/service/start", `{}`, "Bearer secret", "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "unsupported schemaVersion") {
		t.Fatalf("failed start response: %d %s", w.Code, w.Body.String())
	}
}

func TestManagementLogsKeepLatestFiveHundredAndHonorCursor(t *testing.T) {
	a := New(t.TempDir(), "secret", config.Default())
	defer a.Close()
	for i := 0; i < 505; i++ {
		a.log("TEST", "client", "host", fmt.Sprintf("event-%d", i+1), "")
	}
	items, last := a.Logs(0)
	if len(items) != 500 || items[0].ID != 6 || items[499].ID != 505 || last != 505 {
		t.Fatalf("log retention: count=%d first=%d last=%d cursor=%d", len(items), items[0].ID, items[len(items)-1].ID, last)
	}
	w := apiCall(a, http.MethodGet, "/api/logs?after=503", "", "Bearer secret", "")
	var result struct {
		Items  []Event `json:"items"`
		LastID uint64  `json:"lastID"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.LastID != 505 || len(result.Items) != 2 || result.Items[0].ID != 504 {
		t.Fatalf("cursor logs: %d %+v (%v) %s", w.Code, result, err, w.Body.String())
	}
	w = apiCall(a, http.MethodGet, "/api/logs?after=bad", "", "Bearer secret", "")
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Items) != 500 {
		t.Fatalf("invalid cursor should return retained logs: %d items=%d err=%v", w.Code, len(result.Items), err)
	}
}

func TestApplyAndUpdatePersistenceFailuresPreserveState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		offline bool
	}{{"apply running", false}, {"update download toggle", true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "config.json"), 0700); err != nil {
				t.Fatal(err)
			}
			c := config.Default()
			c.LANIP = "127.0.0.1"
			c.DNSListen, c.HTTPListen, c.WebListen = unused(t), unused(t), unused(t)
			c.TLSListen = unused(t)
			c.OfflineEnabled = tc.offline
			a := New(dir, "secret", c)
			defer a.Close()
			if tc.name == "apply running" {
				if err := a.Start(); err != nil {
					t.Fatal(err)
				}
				w := apiCall(a, http.MethodPost, "/api/apply", `{"group":"cn1","ip":"1.1.1.1"}`, "Bearer secret", "")
				if w.Code != http.StatusBadRequest {
					t.Fatalf("Apply response = %d %s", w.Code, w.Body.String())
				}
				if !a.Status().Running || a.Config().Groups["cn1"].IP != "" {
					t.Fatalf("failed Apply changed state: running=%t config=%+v", a.Status().Running, a.Config().Groups["cn1"])
				}
			} else {
				old := a.Config()
				previousManager := a.downloads
				next := config.Clone(old)
				next.OfflineEnabled = false
				body, err := json.Marshal(next)
				if err != nil {
					t.Fatal(err)
				}
				w := apiCall(a, http.MethodPut, "/api/config", string(body), "Bearer secret", "")
				if w.Code != http.StatusBadRequest {
					t.Fatalf("Update response = %d %s", w.Code, w.Body.String())
				}
				if a.Config().OfflineEnabled != old.OfflineEnabled || a.downloads != previousManager || a.Status().Running {
					t.Fatalf("failed Update changed state: config=%+v manager=%v status=%+v", a.Config(), a.downloads, a.Status())
				}
			}
		})
	}
}

func TestDownloadRoutesWhenDisabledOrLibraryUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		enabled, failLibrary     bool
		want                     int
	}{
		{"disabled listing", http.MethodGet, "/api/downloads", "", false, false, 200},
		{"disabled create", http.MethodPost, "/api/downloads", `{"url":"http://assets1.xboxlive.com/file"}`, false, false, 409},
		{"disabled action", http.MethodPost, "/api/downloads/missing/resume", `{}`, false, false, 409},
		{"disabled delete", http.MethodDelete, "/api/downloads/missing", "", false, false, 409},
		{"disabled file", http.MethodGet, "/api/downloads/missing/file", "", false, false, 409},
		{"unavailable listing", http.MethodGet, "/api/downloads", "", true, true, 503},
		{"unavailable create", http.MethodPost, "/api/downloads", `{"url":"http://assets1.xboxlive.com/file"}`, true, true, 503},
		{"unavailable action", http.MethodPost, "/api/downloads/missing/resume", `{}`, true, true, 503},
		{"unavailable delete", http.MethodDelete, "/api/downloads/missing", "", true, true, 503},
		{"unavailable file", http.MethodGet, "/api/downloads/missing/file", "", true, true, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			c := config.Default()
			c.OfflineEnabled = tc.enabled
			downloadDir := filepath.Join(dir, "downloads")
			if tc.failLibrary {
				if err := os.WriteFile(downloadDir, []byte("block directory creation"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			a := NewWithDownloads(filepath.Join(dir, "data"), "secret", c, downloadDir)
			defer a.Close()
			w := apiCall(a, tc.method, tc.path, tc.body, "Bearer secret", "")
			if w.Code != tc.want {
				t.Fatalf("download route = %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
			if tc.name == "disabled listing" && !strings.Contains(w.Body.String(), `"enabled":false`) {
				t.Fatalf("disabled status missing: %s", w.Body.String())
			}
			if tc.failLibrary && !strings.Contains(w.Body.String(), "not a directory") {
				t.Fatalf("library failure not exposed: %s", w.Body.String())
			}
		})
	}
}

func TestOfflineTaskActionErrorResponses(t *testing.T) {
	c := config.Default()
	c.OfflineEnabled = true
	a := New(t.TempDir(), "secret", c)
	defer a.Close()
	for _, tc := range []struct {
		id, action string
		status     int
		message    string
	}{
		{"missing", "resume", 404, "download task not found"},
		{"missing", "unknown", 404, "download task not found"},
	} {
		w := apiCall(a, http.MethodPost, "/api/downloads/"+tc.id+"/"+tc.action, `{}`, "Bearer secret", "")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.message) {
			t.Fatalf("action %s: %d %s", tc.action, w.Code, w.Body.String())
		}
	}
}

func TestManagementOriginHostWithPort(t *testing.T) {
	a := New(t.TempDir(), "secret", config.Default())
	defer a.Close()
	r := httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/status", nil)
	r.Host = "localhost:8080"
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Origin", "http://localhost:8080")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("same host and port rejected: %d %s", w.Code, w.Body.String())
	}
}
