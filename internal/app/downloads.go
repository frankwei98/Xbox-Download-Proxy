package app

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"time"
	"xboxdownloadproxy/internal/domain"
	"xboxdownloadproxy/internal/offline"
)

func downloadProblem(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, offline.ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, offline.ErrConflict) {
		status = http.StatusConflict
	}
	problem(w, status, err)
}

func (a *App) downloadRoutes(api *http.ServeMux) {
	// Keep a library alive for each operation while configuration toggles it.
	route := func(pattern string, handler http.HandlerFunc) {
		api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			a.downloadMu.RLock()
			defer a.downloadMu.RUnlock()
			if !a.Config().OfflineEnabled {
				if r.Method == "GET" && r.URL.Path == "/api/downloads" {
					writeJSON(w, 200, map[string]any{"tasks": []any{}, "storage": nil, "delivery": map[string]any{"enabled": false, "running": a.Status().Running, "hits": 0, "bytes": 0}})
				} else {
					problem(w, 409, errors.New("offline downloads are disabled; enable them in service settings first"))
				}
				return
			}
			if a.downloadErr != nil {
				problem(w, 503, a.downloadErr)
				return
			}
			handler(w, r)
		})
	}
	route("GET /api/downloads", func(w http.ResponseWriter, r *http.Request) {
		storage, err := a.downloads.Storage()
		if err != nil {
			problem(w, 503, err)
			return
		}
		type taskView struct {
			offline.Task
			LocalReady bool `json:"localReady"`
		}
		tasks := []taskView{}
		for _, task := range a.downloads.List() {
			tasks = append(tasks, taskView{task, a.downloads.CanDeliver(task.ID)})
		}
		status := a.Status()
		writeJSON(w, 200, map[string]any{"tasks": tasks, "storage": storage, "delivery": map[string]any{
			"enabled": a.Config().OfflineEnabled, "running": status.Running,
			"hits": status.Metrics.LocalHits, "bytes": status.Metrics.LocalBytes,
		}})
	})
	route("POST /api/downloads", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		}
		if err := decode(w, r, &req); err != nil {
			problem(w, 400, err)
			return
		}
		original := req.URL
		// Match the Xbox HTTP redirect path without looping through our LAN listener.
		if u, err := url.Parse(req.URL); err == nil && u.Scheme == "http" {
			cfg := a.Config()
			if domain.GroupFor(u.Hostname()) == "global" && cfg.RedirectToCN && cfg.Groups["global"].IP == "" {
				if host := domain.Redirect(cfg, u.Hostname()); host != "" {
					u.Host = host
					req.URL = u.String()
				}
			}
		}
		task, err := a.downloads.AddFrom(original, req.URL, req.Name)
		if err != nil {
			downloadProblem(w, err)
			return
		}
		a.log("DOWNLOAD", "", "", "已创建离线下载任务："+task.Name, "")
		writeJSON(w, 202, task)
	})
	route("POST /api/downloads/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		task, err := a.downloads.Action(r.PathValue("id"), r.PathValue("action"))
		if err != nil {
			downloadProblem(w, err)
			return
		}
		writeJSON(w, 200, task)
	})
	route("DELETE /api/downloads/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.downloads.Delete(r.PathValue("id")); err != nil {
			downloadProblem(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	route("GET /api/downloads/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		f, task, err := a.downloads.OpenCompleted(r.PathValue("id"))
		if err != nil {
			downloadProblem(w, err)
			return
		}
		defer f.Close()
		// Large files must not inherit the management server's short write budget.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": task.Name}))
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		http.ServeContent(w, r, task.Name, task.UpdatedAt, f)
	})
}
