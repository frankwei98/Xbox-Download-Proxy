package offline

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const contentPath = "/01234567-89ab-cdef-0123-456789abcdef/1.2.3.4.abcdef01-2345-6789-abcd-ef0123456789_xs.xvc"

func TestResourceLookupSurvivesCDNAliasSignatureAndRestart(t *testing.T) {
	payload := bytes.Repeat([]byte("game-resource"), 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "assets2.xboxlive.cn" || r.URL.RawQuery != "sig=download" {
			t.Errorf("download URL changed: %s %s", r.Host, r.URL)
		}
		w.Write(payload)
	}))
	defer server.Close()
	dir := t.TempDir()
	m := fixtureManager(t, dir, server)
	original := "http://xvcf1.xboxlive.com" + contentPath + "?sig=captured"
	task, err := m.AddFrom(original, "http://assets2.xboxlive.cn"+contentPath+"?sig=download", "user-renamed.bin")
	if err != nil {
		t.Fatal(err)
	}
	complete := await(t, m, task.ID, func(t Task) bool { return t.State == "completed" || t.State == "failed" })
	if complete.State != "completed" || complete.OriginalURL != original || !m.CanDeliver(task.ID) {
		t.Fatalf("not ready: %+v", complete)
	}
	m.Close()
	m = fixtureManager(t, dir, server)
	request := "http://assets1.xboxlive.cn" + contentPath + "?sig=console-renewed"
	f, found, err := m.OpenResource(request)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if found.ID != task.ID || !bytes.Equal(got, payload) {
		t.Fatal("lookup changed data or matched by the local filename")
	}
	for _, raw := range []string{
		strings.Replace(request, "1.2.3.4.", "1.2.3.5.", 1),
		"http://assets1.xboxlive.cn/another/" + strings.TrimPrefix(contentPath[strings.LastIndex(contentPath, "/"):], "/"),
		"http://example.com" + contentPath,
		"https://assets1.xboxlive.cn" + contentPath,
	} {
		if f, _, err := m.OpenResource(raw); !errors.Is(err, ErrNotFound) {
			if f != nil {
				f.Close()
			}
			t.Fatalf("unexpected resource match: %s %v", raw, err)
		}
	}
	if err := os.Truncate(dir+"/"+task.ID+"/"+task.Name, 1); err != nil {
		t.Fatal(err)
	}
	if m.CanDeliver(task.ID) {
		t.Fatal("changed file advertised as ready")
	}
	if f, _, err := m.OpenResource(request); !errors.Is(err, ErrNotFound) {
		if f != nil {
			f.Close()
		}
		t.Fatal("changed file served")
	}
	if err := m.Delete(task.ID); err != nil {
		t.Fatal(err)
	}
	if m.CanDeliver(task.ID) {
		t.Fatal("deleted task is ready")
	}
}

func TestResourceIdentityKeepsUnversionedQueriesAndRejectsOtherOrigins(t *testing.T) {
	a := "http://assets1.xboxlive.com/unversioned/file?version=1"
	b := "http://assets2.xboxlive.cn/unversioned/file?version=2"
	if ResourceKey(a) == ResourceKey(b) {
		t.Fatal("different unversioned resources conflated")
	}
	for _, raw := range []string{
		"http://assets1.xboxlive.com.evil.test" + contentPath,
		"http://assets1.xboxlive.com:8080" + contentPath,
		"http://user@assets1.xboxlive.com" + contentPath,
		"http://dl.delivery.mp.microsoft.com" + contentPath,
		"https://assets1.xboxlive.com" + contentPath,
	} {
		if ResourceKey(raw) != "" {
			t.Fatalf("unsupported origin: %s", raw)
		}
	}
}
