package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoppedBackupRestoresMediaAndAccess(t *testing.T) {
	for _, tc := range []struct {
		name, backend string
		externalDB    bool
	}{{"fs", "fs", false}, {"sqlite", "sqlite", false}, {"sqlite-external", "sqlite", true}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Store = tc.backend
			if tc.externalDB {
				cfg.DBPath = filepath.Join(t.TempDir(), "external", "library.db")
			}
			t.Setenv("PATH", "")
			source, err := newApplication(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if source != nil {
					source.close()
				}
			})
			listID, key := createLockedList(t, source, "Private training")
			publicID := uploadWithAccessList(t, source, "Public training", "", "")
			lockedID := uploadWithAccessList(t, source, "Locked training", "", listID)
			// Seed the on-disk track to exercise the stopped backup workflow.
			dir := filepath.Join(cfg.VideoDir, lockedID, "subtitles")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "track-en.vtt"), []byte(sampleVTT), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := source.close(); err != nil {
				t.Fatal(err)
			}
			source = nil

			original := cfg
			cfg.VideoDir = filepath.Join(t.TempDir(), "restored-videos")
			if err := os.CopyFS(cfg.VideoDir, os.DirFS(original.VideoDir)); err != nil {
				t.Fatal(err)
			}
			if original.DBPath != "" {
				restoredDBDir := filepath.Join(t.TempDir(), "restored-db")
				if err := os.CopyFS(restoredDBDir, os.DirFS(filepath.Dir(original.DBPath))); err != nil {
					t.Fatal(err)
				}
				cfg.DBPath = filepath.Join(restoredDBDir, filepath.Base(original.DBPath))
			}
			cfg.AdminPass = "" // Restore must reuse the existing administrator.
			restored, err := newApplication(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.close() })
			loginRequest(t, restored.routes(), "tester", original.AdminPass)
			public := "/videos/" + publicID + "/original.mp4"
			locked := "/videos/" + lockedID + "/original.mp4"
			subtitle := "/videos/" + lockedID + "/subtitles/track-en.vtt"
			watch := "/watch?id=" + lockedID
			for _, check := range []struct {
				path string
				want int
			}{{"/readyz", 200}, {public, 200}, {locked, 404}, {subtitle, 404}, {watch, 404}} {
				if w := serve(t, restored, http.MethodGet, check.path); w.Code != check.want {
					t.Fatalf("%s: got %d, want %d", check.path, w.Code, check.want)
				}
			}
			granted := serve(t, restored, http.MethodGet, "/view_list?key="+key)
			if granted.Code != 200 {
				t.Fatalf("restored list key returned %d", granted.Code)
			}
			cookie := grantCookie(t, granted)
			if w := serve(t, restored, http.MethodGet, watch, cookie); w.Code != 200 || !strings.Contains(w.Body.String(), subtitle) {
				t.Fatalf("restored watch page returned %d or omitted subtitle", w.Code)
			}
			if w := serve(t, restored, http.MethodGet, locked, cookie); w.Code != 200 || w.Body.Len() != len(sampleVideo(t)) {
				t.Fatalf("restored locked original returned %d and %d bytes", w.Code, w.Body.Len())
			}
			if w := serve(t, restored, http.MethodGet, subtitle, cookie); w.Code != 200 || w.Body.String() != sampleVTT {
				t.Fatalf("restored subtitle returned %d", w.Code)
			}
			rangeRequest := httptest.NewRequest(http.MethodGet, locked, nil)
			rangeRequest.Header.Set("Range", "bytes=0-15")
			rangeRequest.AddCookie(cookie)
			w := httptest.NewRecorder()
			restored.routes().ServeHTTP(w, rangeRequest)
			if w.Code != 206 || w.Body.Len() != 16 {
				t.Fatalf("restored original Range returned %d and %d bytes", w.Code, w.Body.Len())
			}
		})
	}
}
