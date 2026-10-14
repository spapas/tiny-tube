package main

import (
	"bytes"
	"context"
	"errors"
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
	a := &application{cfg: cfg, videoDir: root, ffmpegBin: bin, fs: store.NewFS(root), wake: make(chan struct{}, cfg.TranscodeWorkers)}
	a.runFFmpeg = func(ctx context.Context, bin string, args []string) error {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Stdout, cmd.Stderr = log.Writer(), log.Writer()
		return cmd.Run()
	}
	if err = a.recoverJobs(); err != nil {
		return nil, err
	}
	return a, nil
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
	mux.Handle("GET /videos/", http.StripPrefix("/videos/", http.FileServer(http.Dir(a.videoDir))))
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
