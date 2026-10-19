package app

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"xboxdownloadproxy/internal/config"
)

func unused(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	return l.Addr().String()
}
func TestManagementLifecycle(t *testing.T) {
	c := config.Default()
	c.LANIP = "127.0.0.1"
	c.DNSListen = unused(t)
	c.HTTPListen = unused(t)
	c.TLSListen = ""
	c.WebListen = unused(t)
	dir := t.TempDir()
	if e := config.Save(dir, c); e != nil {
		t.Fatal(e)
	}
	a := New(dir, "test-token", c)
	defer a.Close()
	h := a.Handler()
	call := func(method, path string, body any, auth, origin string) *httptest.ResponseRecorder {
		var b bytes.Buffer
		if body != nil {
			json.NewEncoder(&b).Encode(body)
		}
		r := httptest.NewRequest(method, "http://localhost"+path, &b)
		r.Header.Set("Authorization", auth)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/api/config", nil, "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/api/service/start", nil, "Bearer test-token", "http://evil.test"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/api/service/start", nil, "Bearer test-token", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if !a.Status().Running {
		t.Fatal("not running")
	}
	if w := call("PUT", "/api/config", c, "Bearer test-token", ""); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/api/apply", map[string]string{"group": "cn1", "ip": "1.1.1.1"}, "Bearer test-token", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if a.Status().Running || a.Config().Groups["cn1"].IP != "1.1.1.1" {
		t.Fatal("apply lifecycle")
	}
	if w := call("GET", "/", nil, "", ""); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("Xbox")) {
		t.Fatal("embedded UI unavailable")
	}
	if w := call("POST", "/api/tests", map[string]any{"group": "store", "ips": []string{"1.1.1.1"}}, "Bearer test-token", ""); w.Code != 400 {
		t.Fatal("missing Store test URL not reported")
	}
	var _ http.Handler = h
}
