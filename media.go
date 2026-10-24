package main

import (
	"errors"
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
	case ".mp4", ".mov", ".mkv", ".webm", ".m3u8", ".ts", ".jpg", ".jpeg", ".png":
		return true
	}
	return false
}

type publicMediaFS struct {
	root         *os.Root
	privatePaths func() []string
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
	for _, privatePath := range s.privatePaths() {
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
	if id, _, _ := strings.Cut(name, "/"); id != "" {
		access, err := a.mediaAccessList(id)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrInvalid) {
				http.NotFound(w, r)
			} else {
				writeAPIError(w, r, http.StatusServiceUnavailable, "media authorization unavailable")
			}
			return
		}
		if access != "" {
			grants := a.resolveGrants(w, r)
			u := currentUser(r)
			if u == nil {
				u = a.requestUserLazy(w, r)
			}
			if !canViewList(store.Meta{AccessList: access}, u, grants) {
				http.NotFound(w, r)
				return
			}
			// Private content must not linger in shared caches.
			w.Header().Set("Cache-Control", "no-store")
		}
	}
	a.mediaServer.ServeHTTP(w, r)
}

// mediaAccessList resolves a video's access-list id for media serving. It uses
// a small in-process cache so each HLS segment, poster and Range request does
// not hit metadata storage. Fills and writes share a lock so older reads cannot
// replace newer authorization policies. Metadata-less orphans fail closed.
func (a *application) mediaAccessList(id string) (string, error) {
	a.accessMu.RLock()
	v, ok := a.accessCache[id]
	a.accessMu.RUnlock()
	if ok {
		return v, nil
	}
	a.accessMu.Lock()
	defer a.accessMu.Unlock()
	if v, ok := a.accessCache[id]; ok {
		return v, nil
	}
	m, err := a.fs.Get(id)
	if err != nil {
		return "", err
	}
	a.accessCache[id] = m.AccessList
	return m.AccessList, nil
}

// putMeta centralizes video metadata writes so a visibility change invalidates
// the media access cache instead of leaving a stale public/locked decision.
func (a *application) putMeta(m store.Meta) error {
	a.accessMu.Lock()
	defer a.accessMu.Unlock()
	if err := a.fs.Put(m); err != nil {
		return err
	}
	a.accessCache[m.ID] = m.AccessList
	return nil
}

func (a *application) deleteMeta(id string) error {
	a.accessMu.Lock()
	defer a.accessMu.Unlock()
	if err := a.fs.Delete(id); err != nil {
		return err
	}
	delete(a.accessCache, id)
	return nil
}

// privateMediaPaths lists the database files that must never be served as media.
func privateMediaPaths(dbPath string) []string {
	if dbPath == "" {
		return nil
	}
	paths := make([]string, 0, 4)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		paths = append(paths, dbPath+suffix)
	}
	return paths
}
