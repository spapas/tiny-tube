package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDirUsageSumsFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.bin"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.bin"), make([]byte, 23), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := dirUsage(root)
	if err != nil || got != 123 {
		t.Fatalf("dirUsage = %d, %v", got, err)
	}
	if _, err := dirUsage(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing root should fail closed")
	}
}

func TestQuotaValidation(t *testing.T) {
	t.Setenv("VIDEO_DIR_QUOTA_BYTES", "1073741824")
	cfg, err := loadConfig()
	if err != nil || cfg.QuotaBytes != 1073741824 {
		t.Fatalf("quota = %d, %v", cfg.QuotaBytes, err)
	}
	for _, bad := range []string{"-1", "ten", "1.5"} {
		t.Setenv("VIDEO_DIR_QUOTA_BYTES", bad)
		if _, err := loadConfig(); err == nil {
			t.Fatalf("quota %q accepted", bad)
		}
	}
}

func testQuotaApplication(t *testing.T, quota int64) *application {
	t.Helper()
	cfg := testConfig(t)
	cfg.QuotaBytes = quota
	a, err := newApplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.ffmpegBin = ""
	t.Cleanup(func() { a.close() })
	return a
}

func TestUploadRejectedWhenQuotaExceeded(t *testing.T) {
	a := testQuotaApplication(t, 1)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, uploadRequest(t, a, "Too big", "original.mp4", sampleVideo(t)))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("quota upload: %d %s", w.Code, w.Body.String())
	}
}

func TestUploadAllowedWithoutQuota(t *testing.T) {
	a := testQuotaApplication(t, 0)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, uploadRequest(t, a, "Fine", "original.mp4", sampleVideo(t)))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("unlimited upload: %d %s", w.Code, w.Body.String())
	}
}
