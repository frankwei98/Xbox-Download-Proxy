package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveDownloadStreamsAndRefusesOverwrite(t *testing.T) {
	payload := bytes.Repeat([]byte("saved-file"), 128*1024)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(401)
			return
		}
		w.Write(payload)
	}))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "game.bin")
	if err := saveDownload(s.URL, "secret", "/file", path); err != nil {
		t.Fatal(err)
	}
	if err := saveDownload(s.URL, "secret", "/file", path); err == nil {
		t.Fatal("overwrote existing file")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("CLI saved file differs")
	}
	failed := filepath.Join(t.TempDir(), "unauthorized.bin")
	if err := saveDownload(s.URL, "wrong", "/file", failed); err == nil {
		t.Fatal("missing auth accepted")
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatal("failed request left an output file")
	}
}
