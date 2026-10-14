package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"tiny-tube/store"
)

type application struct {
	cfg                 config
	videoDir, ffmpegBin string
	fs                  store.Store
	wake                chan struct{}
	claimMu             sync.Mutex
	workers             sync.WaitGroup
	runFFmpeg           func(context.Context, string, []string) error
	activeJobs          map[string]*conversionJob
	deleting            map[string]bool
}

type conversionJob struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func newApplication(cfg config) (*application, error) {
	root, err := filepath.Abs(cfg.VideoDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if err = checkWritable(root); err != nil {
		return nil, err
	}
	bin, err := resolveFFmpeg(cfg.FFmpegBin)
	if err != nil {
		return nil, err
	}
	a := &application{cfg: cfg, videoDir: root, ffmpegBin: bin, wake: make(chan struct{}, cfg.TranscodeWorkers), activeJobs: make(map[string]*conversionJob), deleting: make(map[string]bool)}
	switch cfg.Store {
	case "fs":
		a.fs = store.NewFS(root)
	case "sqlite", "":
		dbPath := cfg.DBPath
		if dbPath == "" {
			dbPath = filepath.Join(root, "tiny-tube.db")
		}
		a.cfg.DBPath, err = filepath.Abs(dbPath)
		if err != nil {
			return nil, err
		}
		db, err := store.NewSQLite(root, a.cfg.DBPath)
		if err != nil {
			return nil, err
		}
		a.fs = db
		if count, err := store.ImportFS(db, root); err != nil {
			db.Close()
			return nil, err
		} else if count > 0 {
			log.Printf("imported %d legacy metadata records", count)
		}
	default:
		return nil, fmt.Errorf("STORE must be sqlite or fs")
	}
	a.runFFmpeg = func(ctx context.Context, bin string, args []string) error {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Stdout, cmd.Stderr = log.Writer(), log.Writer()
		return cmd.Run()
	}
	if err = a.recoverJobs(); err != nil {
		a.close()
		return nil, err
	}
	return a, nil
}

func (a *application) close() error {
	if closer, ok := a.fs.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func checkWritable(root string) error {
	f, err := os.CreateTemp(root, ".ready-*")
	if err != nil {
		return err
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	return errors.Join(closeErr, removeErr)
}

func (a *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := checkWritable(a.videoDir); err != nil {
			log.Printf("storage readiness: %v", err)
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}
		if _, err := a.fs.List(""); err != nil {
			log.Printf("metadata readiness: %v", err)
			http.Error(w, "metadata unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /{$}", a.handleList)
	mux.HandleFunc("GET /watch", a.handleWatch)
	mux.Handle("GET /upload", uploadAuth(a.cfg.UploadUser, a.cfg.UploadPass, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		render(w, uploadTmpl, map[string]any{"Page": "upload", "Title": "Ανέβασμα βίντεο", "MaxUploadBytes": a.cfg.MaxUploadBytes})
	})))
	mux.Handle("POST /api/upload", uploadAuth(a.cfg.UploadUser, a.cfg.UploadPass, http.HandlerFunc(a.handleUpload)))
	mux.Handle("GET /edit", uploadAuth(a.cfg.UploadUser, a.cfg.UploadPass, http.HandlerFunc(a.handleEditPage)))
	mux.Handle("POST /api/edit", uploadAuth(a.cfg.UploadUser, a.cfg.UploadPass, http.HandlerFunc(a.handleEdit)))
	mux.Handle("POST /api/delete", uploadAuth(a.cfg.UploadUser, a.cfg.UploadPass, http.HandlerFunc(a.handleDelete)))
	mux.HandleFunc("GET /api/videos", a.handleAPIVideos)
	mux.HandleFunc("GET /api/video", a.handleAPIVideo)
	media := http.StripPrefix("/videos/", http.FileServer(http.Dir(a.videoDir)))
	mux.HandleFunc("GET /videos/", func(w http.ResponseWriter, r *http.Request) {
		// The default DB is under VIDEO_DIR. Only files inside video ID directories
		// are public; SQLite databases, their journals and staging files stay private.
		rel := strings.TrimPrefix(r.URL.Path, "/videos/")
		file := filepath.Join(a.videoDir, filepath.FromSlash(rel))
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			if a.cfg.DBPath != "" && strings.EqualFold(file, a.cfg.DBPath+suffix) {
				http.NotFound(w, r)
				return
			}
		}
		parts := strings.Split(rel, "/")
		if len(parts) < 2 || !store.ValidID(parts[0]) {
			http.NotFound(w, r)
			return
		}
		for _, part := range parts[1:] {
			if part == "" || strings.HasPrefix(part, ".") {
				http.NotFound(w, r)
				return
			}
		}
		ext := strings.ToLower(filepath.Ext(rel))
		switch ext {
		case ".mp4", ".mov", ".mkv", ".webm", ".m3u8", ".ts", ".jpg", ".jpeg", ".png", ".json":
			media.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(staticFiles()))))
	return mux
}

func render(w http.ResponseWriter, tmpl *template.Template, data any) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.Printf("render %s: %v", tmpl.Name(), err)
		http.Error(w, "page rendering failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("write page: %v", err)
	}
}

func (a *application) serverError(w http.ResponseWriter, operation string, err error) {
	log.Printf("%s: %v", operation, err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// wantsJSON reports whether the client prefers a JSON REST response over the
// browser HTML/redirect behavior. Explicit ?format=json wins; otherwise any
// Accept header mentioning application/json opts in. Browsers sending
// text/html keep the existing redirects.
func wantsJSON(r *http.Request) bool {
	if r.URL.Query().Get("format") == "json" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write JSON: %v", err)
	}
}

// writeAPIError mirrors http.Error but returns {"error": msg} for JSON clients.
func writeAPIError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	if wantsJSON(r) {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	http.Error(w, msg, code)
}

func (a *application) apiServerError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	log.Printf("%s: %v", operation, err)
	writeAPIError(w, r, http.StatusInternalServerError, "internal server error")
}

// GET /api/videos?q=&category= — public JSON mirror of the library page,
// so API clients (including LLMs) can list videos and poll conversion status.
func (a *application) handleAPIVideos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	category := r.URL.Query().Get("category")
	all, err := a.fs.List("")
	if err != nil {
		a.apiServerError(w, r, "list videos", err)
		return
	}
	items := make([]store.Meta, 0, len(all))
	query := strings.ToLower(strings.TrimSpace(q))
	for _, m := range all {
		if category != "" && m.Category != category {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(m.Title+" "+m.Desc+" "+m.Category), query) {
			continue
		}
		items = append(items, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": len(all), "count": len(items),
		"q": q, "category": category, "items": items,
	})
}

// GET /api/video?id= — public JSON metadata for one video.
func (a *application) handleAPIVideo(w http.ResponseWriter, r *http.Request) {
	m, err := a.fs.Get(r.URL.Query().Get("id"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrInvalid) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		} else {
			a.apiServerError(w, r, "read video", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *application) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	category := r.URL.Query().Get("category")
	all, err := a.fs.List("")
	if err != nil {
		a.serverError(w, "list videos", err)
		return
	}
	type categoryFilter struct {
		Name     string
		Count    int
		Selected bool
	}
	counts := make(map[string]int)
	items := make([]store.Meta, 0, len(all))
	query := strings.ToLower(strings.TrimSpace(q))
	for _, m := range all {
		if m.Category != "" {
			counts[m.Category]++
		}
		if category != "" && m.Category != category {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(m.Title+" "+m.Desc+" "+m.Category), query) {
			continue
		}
		items = append(items, m)
	}
	categories := make([]categoryFilter, 0, len(counts))
	for name, count := range counts {
		categories = append(categories, categoryFilter{name, count, name == category})
	}
	sort.Slice(categories, func(i, j int) bool { return categories[i].Name < categories[j].Name })
	render(w, listTmpl, map[string]any{"Page": "list", "Title": "Βιβλιοθήκη", "Items": items, "Q": q, "Category": category, "Categories": categories, "Total": len(all)})
}

func (a *application) handleWatch(w http.ResponseWriter, r *http.Request) {
	m, err := a.fs.Get(r.URL.Query().Get("id"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrInvalid) {
			http.NotFound(w, r)
		} else {
			a.serverError(w, "read video", err)
		}
		return
	}
	render(w, watchTmpl, map[string]any{"Page": "watch", "Title": m.Title, "Video": m})
}
