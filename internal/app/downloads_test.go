package app

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"xboxdownloadproxy/internal/config"
	"xboxdownloadproxy/internal/offline"
)

func TestOfflineManagementAndAuthenticatedFileRanges(t *testing.T) {
	dir := t.TempDir()
	id := strings.Repeat("a", 32)
	root := filepath.Join(dir, "downloads")
	if err := os.MkdirAll(filepath.Join(root, id), 0700); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("verified-download"), 1024)
	name := "游戏下载.bin"
	if err := os.WriteFile(filepath.Join(root, id, name), payload, 0600); err != nil {
		t.Fatal(err)
	}
	task := offline.Task{ID: id, Name: name, URL: "https://cdn.example/game", State: "completed", Bytes: int64(len(payload)), TotalBytes: int64(len(payload)), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	b, _ := json.Marshal([]offline.Task{task})
	if err := os.WriteFile(filepath.Join(root, "tasks.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	a := New(dir, "secret", config.Default())
	defer a.Close()
	if a.DownloadError() != nil {
		t.Fatal(a.DownloadError())
	}
	h := a.Handler()
	call := func(method, path, body, token, rangeHeader string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Range", rangeHeader)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/api/downloads/"+id+"/file", "", "", ""); w.Code != 401 {
		t.Fatal("file route bypassed auth")
	}
	w := call("GET", "/api/downloads/"+id+"/file", "", "secret", "bytes=12-42")
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), payload[12:43]) || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("file range %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("downloaded HTML could run in management origin")
	}
	w = call("GET", "/api/downloads", "", "secret", "")
	var data struct {
		Tasks   []offline.Task  `json:"tasks"`
		Storage offline.Storage `json:"storage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || w.Code != 200 || len(data.Tasks) != 1 || data.Storage.DownloadBytes != uint64(len(payload)) {
		t.Fatalf("listing %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{"url":"http://127.0.0.1/private"}`, `{"url":"http://cdn.example/file","name":"../escape"}`} {
		if w := call("POST", "/api/downloads", body, "secret", ""); w.Code != 400 {
			t.Fatalf("unsafe source accepted %d", w.Code)
		}
	}
	if w := call("POST", "/api/downloads/"+id+"/resume", "{}", "secret", ""); w.Code != 409 {
		t.Fatal("completed task restarted")
	}
	if w := call("DELETE", "/api/downloads/"+id, "", "secret", ""); w.Code != 204 {
		t.Fatalf("delete %d %s", w.Code, w.Body.String())
	}
	if w := call("GET", "/api/downloads/"+id+"/file", "", "secret", ""); w.Code != 404 {
		t.Fatal("deleted file still served")
	}
}
