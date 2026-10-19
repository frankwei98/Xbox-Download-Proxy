package app

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"xboxdownloadproxy/internal/config"
)

//go:embed web/*
var assets embed.FS

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, e error) {
	writeJSON(w, status, map[string]string{"error": e.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	api := http.NewServeMux()
	a.downloadRoutes(api)
	api.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.Status()) })
	api.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.Config()) })
	api.HandleFunc("PUT /api/config", func(w http.ResponseWriter, r *http.Request) {
		var c config.Config
		if e := decode(w, r, &c); e != nil {
			problem(w, 400, e)
			return
		}
		if e := a.Update(c); e != nil {
			status := 400
			if errors.Is(e, ErrRunning) {
				status = 409
			}
			problem(w, status, e)
			return
		}
		writeJSON(w, 200, a.Config())
	})
	api.HandleFunc("POST /api/service/start", func(w http.ResponseWriter, r *http.Request) {
		if e := a.Start(); e != nil {
			problem(w, 409, e)
			return
		}
		writeJSON(w, 200, a.Status())
	})
	api.HandleFunc("POST /api/service/stop", func(w http.ResponseWriter, r *http.Request) { a.Stop(); writeJSON(w, 200, a.Status()) })
	api.HandleFunc("GET /api/groups", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.Groups()) })
	api.HandleFunc("GET /api/candidates", func(w http.ResponseWriter, r *http.Request) {
		l, e := a.Candidates(r.Context(), r.URL.Query().Get("group"), r.URL.Query().Get("refresh") == "true")
		if e != nil {
			problem(w, 502, e)
			return
		}
		writeJSON(w, 200, l)
	})
	api.HandleFunc("POST /api/candidates/import", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Group string `json:"group"`
			Text  string `json:"text"`
		}
		if e := decode(w, r, &req); e != nil {
			problem(w, 400, e)
			return
		}
		l, e := a.candidates.Import(req.Group, req.Text)
		if e != nil {
			problem(w, 400, e)
			return
		}
		writeJSON(w, 200, l)
	})
	api.HandleFunc("POST /api/apply", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Group string `json:"group"`
			IP    string `json:"ip"`
		}
		if e := decode(w, r, &req); e != nil {
			problem(w, 400, e)
			return
		}
		c, e := a.Apply(req.Group, req.IP)
		if e != nil {
			problem(w, 400, e)
			return
		}
		writeJSON(w, 200, map[string]any{"config": c, "running": false})
	})
	api.HandleFunc("POST /api/tests", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Group   string   `json:"group"`
			IPs     []string `json:"ips"`
			TestURL string   `json:"testURL"`
		}
		if e := decode(w, r, &req); e != nil {
			problem(w, 400, e)
			return
		}
		job, e := a.Test(req.Group, req.TestURL, req.IPs)
		if e != nil {
			problem(w, 400, e)
			return
		}
		writeJSON(w, 202, job)
	})
	api.HandleFunc("GET /api/tests/{id}", func(w http.ResponseWriter, r *http.Request) {
		job, ok := a.tests.Get(r.PathValue("id"))
		if !ok {
			problem(w, 404, errors.New("test not found"))
			return
		}
		writeJSON(w, 200, job)
	})
	api.HandleFunc("POST /api/tests/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		job, ok := a.tests.Cancel(r.PathValue("id"))
		if !ok {
			problem(w, 404, errors.New("test not found"))
			return
		}
		writeJSON(w, 200, job)
	})
	api.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		items, last := a.Logs(after)
		writeJSON(w, 200, map[string]any{"items": items, "lastID": last})
	})
	api.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { problem(w, 404, errors.New("API endpoint not found")) })
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || a.token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(a.token)) != 1 {
			problem(w, 401, errors.New("management token required"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				problem(w, 403, errors.New("cross-origin management request rejected"))
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		api.ServeHTTP(w, r)
	}))
	sub, _ := fs.Sub(assets, "web")
	files := http.FileServer(http.FS(sub))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/style.css" {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}
