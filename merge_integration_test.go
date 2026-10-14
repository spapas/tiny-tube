package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tiny-tube/store"
)

func testBackendApplication(t *testing.T, backend string) *application {
	t.Helper()
	cfg := testConfig(t)
	cfg.Store = backend
	a, err := newApplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.ffmpegBin = ""
	t.Cleanup(func() {
		if err := a.close(); err != nil {
			t.Error(err)
		}
	})
	return a
}

func TestBothBackendsUploadEditRangeAndDelete(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testBackendApplication(t, backend)
			h := a.routes()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, uploadRequest(t, a, "ΑΡΧΙΚΟΣ", "sample.mp4", sampleVideo(t)))
			if w.Code != 303 {
				t.Fatal(w.Code, w.Body.String())
			}
			id := strings.TrimPrefix(w.Header().Get("Location"), "/watch?id=")
			for _, tc := range []struct{ method, path string }{{"GET", "/edit?id=" + id}, {"POST", "/api/edit"}, {"POST", "/api/delete"}} {
				w = httptest.NewRecorder()
				r := httptest.NewRequest(tc.method, tc.path, nil)
				r.Header.Set("Accept", "application/json")
				h.ServeHTTP(w, r)
				if w.Code != 401 {
					t.Errorf("unauthenticated %s: %d", tc.path, w.Code)
				}
			}
			// Editors can edit but not delete; admins can do both.
			testUser(t, a, "ed", store.RoleEditor)
			w, forbidden := postForm(t, "/api/delete", url.Values{"id": {id}})
			forbidden.AddCookie(testSession(t, a, "ed"))
			forbidden.Header.Set("Accept", "application/json")
			h.ServeHTTP(w, forbidden)
			if w.Code != 403 {
				t.Fatalf("editor delete: %d", w.Code)
			}
			w, edit := postForm(t, "/api/edit", url.Values{"id": {id}, "title": {"ΕΝΗΜΕΡΩΣΗ"}, "desc": {"new"}, "category": {"ΟΜΑΔΑ"}})
			edit.AddCookie(testSession(t, a, "ed"))
			h.ServeHTTP(w, edit)
			if w.Code != 303 {
				t.Fatal(w.Code, w.Body.String())
			}
			items, err := a.fs.List("ενημερωση")
			if err != nil || len(items) != 1 || items[0].Title != "ΕΝΗΜΕΡΩΣΗ" {
				t.Fatalf("Unicode search/edit: %+v %v", items, err)
			}
			w = httptest.NewRecorder()
			rangeReq := httptest.NewRequest("GET", "/videos/"+id+"/original.mp4", nil)
			rangeReq.Header.Set("Range", "bytes=0-99")
			h.ServeHTTP(w, rangeReq)
			if w.Code != 206 || w.Body.Len() != 100 {
				t.Fatal("Range", w.Code, w.Body.Len())
			}
			for _, path := range []string{"/watch?id=missing", "/videos/", "/videos/tiny-tube.db", "/videos/tiny-tube.db-wal"} {
				w = httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				if w.Code != 404 {
					t.Errorf("%s: %d", path, w.Code)
				}
			}
			w, del := postForm(t, "/api/delete", url.Values{"id": {id}})
			del.AddCookie(testSession(t, a, "tester"))
			h.ServeHTTP(w, del)
			if w.Code != 303 {
				t.Fatal(w.Code, w.Body.String())
			}
			if _, err := a.fs.Get(id); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("deleted metadata: %v", err)
			}
			if _, err := os.Stat(a.fs.Dir(id)); !os.IsNotExist(err) {
				t.Fatalf("deleted files: %v", err)
			}
		})
	}
}

func TestConversionPreservesConcurrentEdit(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testBackendApplication(t, backend)
			a.ffmpegBin = "fake"
			// SQLite metadata does not create the media directory.
			if err := os.MkdirAll(a.fs.Dir("editing"), 0o755); err != nil {
				t.Fatal(err)
			}
			queueVideo(t, a, "editing")
			started, release := make(chan struct{}, 1), make(chan struct{})
			a.runFFmpeg = func(ctx context.Context, bin string, args []string) error {
				if filepath.Ext(args[len(args)-1]) == ".jpg" {
					return fakeHLS(ctx, bin, args)
				}
				started <- struct{}{}
				select {
				case <-release:
					return fakeHLS(ctx, bin, args)
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			startTestWorkers(t, a)
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("job did not start")
			}
			w, r := postForm(t, "/api/edit", url.Values{"id": {"editing"}, "title": {"Updated while converting"}, "desc": {"keep me"}})
			a.handleEdit(w, r)
			if w.Code != 303 {
				t.Fatal(w.Code)
			}
			close(release)
			m := waitStatus(t, a, "editing", "ready")
			if m.Title != "Updated while converting" || m.Desc != "keep me" || m.Poster == "" || m.HLS == "" {
				t.Fatalf("conversion overwrote edits: %+v", m)
			}
		})
	}
}

func TestDeleteCancelsConversionWithoutResurrection(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testBackendApplication(t, backend)
			a.ffmpegBin = "fake"
			if err := os.MkdirAll(a.fs.Dir("deleting"), 0o755); err != nil {
				t.Fatal(err)
			}
			queueVideo(t, a, "deleting")
			started := make(chan struct{}, 1)
			a.runFFmpeg = func(ctx context.Context, _ string, _ []string) error {
				started <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			}
			cancel := startTestWorkers(t, a)
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("job did not start")
			}
			w, r := postForm(t, "/api/delete", url.Values{"id": {"deleting"}})
			a.handleDelete(w, r)
			if w.Code != 303 {
				t.Fatal(w.Code, w.Body.String())
			}
			cancel()
			a.workers.Wait()
			if _, err := a.fs.Get("deleting"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("resurrected metadata: %v", err)
			}
			if _, err := os.Stat(a.fs.Dir("deleting")); !os.IsNotExist(err) {
				t.Fatalf("resurrected files: %v", err)
			}
		})
	}
}

func TestSQLiteStartupImportsLegacyWithoutOverwritingEdits(t *testing.T) {
	cfg := testConfig(t)
	cfg.Store = "sqlite"
	fs := store.NewFS(cfg.VideoDir)
	if err := fs.Put(store.Meta{ID: "legacy", Title: "Old", Status: "ready", Poster: "poster.jpg"}); err != nil {
		t.Fatal(err)
	}
	a, err := newApplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.fs.Get("legacy")
	if err != nil || m.Poster != "poster.jpg" {
		a.close()
		t.Fatalf("import: %+v %v", m, err)
	}
	m.Title = "Edited in SQLite"
	if err := a.fs.Put(m); err != nil {
		a.close()
		t.Fatal(err)
	}
	if err := a.close(); err != nil {
		t.Fatal(err)
	}
	a, err = newApplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	m, err = a.fs.Get("legacy")
	if err != nil || m.Title != "Edited in SQLite" {
		t.Fatalf("restart reverted edit: %+v %v", m, err)
	}
}

func TestConfigAddressPrecedence(t *testing.T) {
	for _, key := range []string{"ADMIN_USER", "ADMIN_PASS", "AUTH_MODE", "HOST", "PORT", "ADDR", "STORE", "MAX_UPLOAD_BYTES", "TRANSCODE_WORKERS", "AUTH_REQUESTS_PER_MINUTE", "UPLOAD_TIMEOUT_SECONDS", "TRANSCODE_TIMEOUT_SECONDS"} {
		t.Setenv(key, "")
	}
	cfg, err := loadConfig()
	if err != nil || cfg.address() != "127.0.0.1:8081" || cfg.Store != "sqlite" {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	t.Setenv("PORT", "8080")
	cfg, err = loadConfig()
	if err != nil || cfg.address() != "127.0.0.1:8080" {
		t.Fatalf("PORT compatibility: %+v %v", cfg, err)
	}
	t.Setenv("ADDR", "[::1]:8090")
	t.Setenv("PORT", "ignored")
	cfg, err = loadConfig()
	if err != nil || cfg.address() != "[::1]:8090" {
		t.Fatalf("ADDR precedence: %+v %v", cfg, err)
	}
	t.Setenv("ADDR", "invalid")
	if _, err := loadConfig(); err == nil {
		t.Fatal("invalid ADDR accepted")
	}
	t.Setenv("ADDR", "127.0.0.1:8081")
	t.Setenv("STORE", "unknown")
	if _, err := loadConfig(); err == nil {
		t.Fatal("unknown STORE accepted")
	}
}
