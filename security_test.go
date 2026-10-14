package main

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"tiny-tube/store"
)

func TestCrossOriginMutationsAreRejected(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testBackendApplication(t, backend)
			original := store.Meta{ID: "protected", Title: "Keep me", Status: "ready"}
			if err := a.fs.Put(original); err != nil {
				t.Fatal(err)
			}
			h := a.routes()
			for _, path := range []string{"/api/upload", "/api/edit", "/api/delete"} {
				for _, tc := range []struct{ name, origin, site string }{
					{"foreign origin", "https://attacker.example", ""},
					{"cross site", "", "cross-site"},
					{"same site different origin", "", "same-site"},
					{"opaque origin", "null", ""},
				} {
					t.Run(path+"/"+tc.name, func(t *testing.T) {
						w, r := postForm(t, path, url.Values{"id": {"protected"}, "title": {"Changed"}})
						r.SetBasicAuth("tester", "test-password")
						r.Header.Set("Origin", tc.origin)
						r.Header.Set("Sec-Fetch-Site", tc.site)
						h.ServeHTTP(w, r)
						if w.Code != 403 {
							t.Fatalf("cross-origin mutation accepted: %d %s", w.Code, w.Body.String())
						}
						m, err := a.fs.Get("protected")
						if err != nil || m.Title != original.Title {
							t.Fatalf("cross-origin mutation changed metadata: %+v %v", m, err)
						}
					})
				}
			}
		})
	}
}

func TestDatabaseHardLinkCannotBeServedAsVideo(t *testing.T) {
	a := testBackendApplication(t, "sqlite")
	dir := filepath.Join(a.videoDir, "alias")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a.cfg.DBPath, filepath.Join(dir, "original.mp4")); err != nil {
		t.Fatal("could not create temporary DB alias", err)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/videos/alias/original.mp4", nil))
	if w.Code != 404 {
		t.Fatalf("database alias was exposed: %d (%d bytes)", w.Code, w.Body.Len())
	}
}
