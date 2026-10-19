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

// acquireDownloads keeps the library alive until the returned release is called.
// An opened file has its own lifetime and can be streamed after releasing it.
func (a *App) acquireDownloads(w http.ResponseWriter, r *http.Request) (*offline.Manager, func()) {
	a.downloadMu.RLock()
	if !a.Config().OfflineEnabled {
		if r.Method == "GET" && r.URL.Path == "/api/downloads" {
			writeJSON(w, 200, map[string]any{"tasks": []any{}, "storage": nil, "delivery": map[string]any{"enabled": false, "running": a.Status().Running, "hits": 0, "bytes": 0}})
		} else {
			problem(w, 409, errors.New("offline downloads are disabled; enable them in service settings first"))
		}
		a.downloadMu.RUnlock()
		return nil, nil
	}
	if a.downloadErr != nil {
		problem(w, 503, a.downloadErr)
		a.downloadMu.RUnlock()
		return nil, nil
	}
	return a.downloads, a.downloadMu.RUnlock
}

func (a *App) downloadRoutes(api *http.ServeMux) {
	// Keep a library alive for each operation while configuration toggles it.
	route := func(pattern string, handler func(http.ResponseWriter, *http.Request, *offline.Manager)) {
		api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			downloads, release := a.acquireDownloads(w, r)
			if downloads == nil {
				return
			}
			defer release()
			handler(w, r, downloads)
		})
	}
	route("GET /api/downloads", func(w http.ResponseWriter, r *http.Request, downloads *offline.Manager) {
		storage, err := downloads.Storage()
		if err != nil {
			problem(w, 503, err)
			return
		}
		type taskView struct {
			offline.Task
			LocalReady bool `json:"localReady"`
		}
		tasks := []taskView{}
		for _, task := range downloads.List() {
			tasks = append(tasks, taskView{task, downloads.CanDeliver(task.ID)})
		}
		status := a.Status()
		writeJSON(w, 200, map[string]any{"tasks": tasks, "storage": storage, "delivery": map[string]any{
			"enabled": a.Config().OfflineEnabled, "running": status.Running,
			"hits": status.Metrics.LocalHits, "bytes": status.Metrics.LocalBytes,
		}})
	})
	route("POST /api/downloads", func(w http.ResponseWriter, r *http.Request, downloads *offline.Manager) {
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
		task, err := downloads.AddFrom(original, req.URL, req.Name)
		if err != nil {
			downloadProblem(w, err)
			return
		}
		a.log("DOWNLOAD", "", "", "已创建离线下载任务："+task.Name, "")
		writeJSON(w, 202, task)
	})
	route("POST /api/downloads/{id}/{action}", func(w http.ResponseWriter, r *http.Request, downloads *offline.Manager) {
		task, err := downloads.Action(r.PathValue("id"), r.PathValue("action"))
		if err != nil {
			downloadProblem(w, err)
			return
		}
		writeJSON(w, 200, task)
	})
	route("DELETE /api/downloads/{id}", func(w http.ResponseWriter, r *http.Request, downloads *offline.Manager) {
		if err := downloads.Delete(r.PathValue("id")); err != nil {
			downloadProblem(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	api.HandleFunc("GET /api/downloads/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		downloads, release := a.acquireDownloads(w, r)
		if downloads == nil {
			return
		}
		f, task, err := downloads.OpenCompleted(r.PathValue("id"))
		release()
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
