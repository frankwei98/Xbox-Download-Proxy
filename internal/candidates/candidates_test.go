package candidates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"xboxdownloadproxy/internal/config"
)

func TestSourcesCacheAndImport(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("XboxCn1\n1.1.1.1 中国 电信\n1.1.1.1 duplicate\n192.168.1.1 rejected\n"))
	}))
	defer srv.Close()
	s := New(t.TempDir())
	s.Client = srv.Client()
	c := config.Default()
	g := c.Groups["cn1"]
	g.SourceURL = srv.URL
	c.Groups["cn1"] = g
	l, e := s.Load(context.Background(), c, "cn1", false)
	if e != nil {
		t.Fatal(e)
	}
	if l.Origin != "online" || len(l.Items) != 1 || l.Items[0].Location != "中国 电信" {
		t.Fatalf("%+v", l)
	}
	srv.Close()
	l, e = s.Load(context.Background(), c, "cn1", true)
	if e != nil || l.Origin != "cache" || l.Warning == "" {
		t.Fatalf("offline fallback: %+v %v", l, e)
	}
	l, e = s.Import("store", "8.8.8.8 imported")
	if e != nil {
		t.Fatal(e)
	}
	l, e = s.Load(context.Background(), c, "store", false)
	if e != nil || l.Items[0].IP != "8.8.8.8" {
		t.Fatalf("import cache lost: %+v %v", l, e)
	}
	if _, e = s.Import("../escape", "1.1.1.1"); e == nil {
		t.Fatal("path traversal group accepted")
	}
	if _, e = Parse("127.0.0.1\nnot-an-ip\n169.254.1.1"); e == nil {
		t.Fatal("private IP list accepted")
	}
}
