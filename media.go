package main

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"tiny-tube/store"
)

func publicMediaPath(name string) bool {
	if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:\x00") {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) < 2 || !store.ValidID(parts[0]) {
		return false
	}
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mov", ".mkv", ".webm", ".m3u8", ".ts", ".jpg", ".jpeg", ".png", ".json":
		return true
	}
	return false
}

type publicMediaFS struct {
	root         *os.Root
	privatePaths []string
}

func (s publicMediaFS) Open(name string) (fs.File, error) {
	if !publicMediaPath(name) {
		return nil, fs.ErrNotExist
	}
	// Root does not follow links outside the media directory, unlike http.Dir.
	f, err := s.root.Open(filepath.FromSlash(name))
	if err != nil {
		return nil, fs.ErrNotExist
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fs.ErrNotExist
	}
	// A DB hard link or in-tree symlink can carry an innocent media extension.
	for _, privatePath := range s.privatePaths {
		if private, err := os.Stat(privatePath); err == nil && os.SameFile(info, private) {
			f.Close()
			return nil, fs.ErrNotExist
		}
	}
	return f, nil
}

func (a *application) serveVideo(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/videos/")
	if !publicMediaPath(name) {
		http.NotFound(w, r)
		return
	}
	// Locked videos are gated by list key even on guessed direct URLs.
	// Metadata-less orphans keep the historical public behavior.
	if id, _, _ := strings.Cut(name, "/"); id != "" {
		if m, err := a.fs.Get(id); err == nil && m.AccessList != "" {
			grants := a.resolveGrants(w, r)
			u := currentUser(r)
			if u == nil {
				u = a.requestUserLazy(r)
			}
			if !canViewList(m, u, grants) {
				http.NotFound(w, r)
				return
			}
			// Private content must not linger in shared caches.
			w.Header().Set("Cache-Control", "no-store")
		}
	}
	root, err := os.OpenRoot(a.videoDir)
	if err != nil {
		a.serverError(w, "open media storage", err)
		return
	}
	defer root.Close()
	var privatePaths []string
	if a.cfg.DBPath != "" {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			privatePaths = append(privatePaths, a.cfg.DBPath+suffix)
		}
	}
	media := http.FileServer(http.FS(publicMediaFS{root: root, privatePaths: privatePaths}))
	http.StripPrefix("/videos/", media).ServeHTTP(w, r)
}
