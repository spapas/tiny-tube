package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tiny-tube/store"
)

func testBackupApplication(t *testing.T, storeName string, keep int) *application {
	t.Helper()
	cfg := testConfig(t)
	cfg.Store = storeName
	cfg.BackupDir = filepath.Join(t.TempDir(), "backups")
	cfg.BackupKeep = keep
	a, err := newApplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.ffmpegBin = ""
	t.Cleanup(func() { a.close() })
	return a
}

func seedBackupVideo(t *testing.T, a *application, id string) {
	t.Helper()
	m := store.Meta{ID: id, Title: "Backup " + id, Original: "original.mp4", Status: "ready", Created: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := a.fs.Put(m); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.videoDir, id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.videoDir, id, "original.mp4"), sampleVideo(t), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listBackupDirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), backupDirPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestBackupSQLiteRoundTrip(t *testing.T) {
	a := testBackupApplication(t, "sqlite", 7)
	seedBackupVideo(t, a, "vid1")
	if err := a.runBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	backups := listBackupDirs(t, a.cfg.BackupDir)
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %v", backups)
	}
	root := filepath.Join(a.cfg.BackupDir, backups[0])
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man backupManifest
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	if man.Version != appVersion || man.Store != "sqlite" || man.DB != "tiny-tube.db" {
		t.Fatalf("bad manifest: %+v", man)
	}
	dbCopy := filepath.Join(root, "videos", "tiny-tube.db")
	if fi, err := os.Stat(dbCopy); err != nil || fi.Size() == 0 {
		t.Fatalf("database copy missing: %v", err)
	}
	// The copy must be a working database with the video metadata.
	restored, err := store.NewSQLite(t.TempDir(), dbCopy)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	videos, err := restored.List("")
	if err != nil || len(videos) != 1 || videos[0].ID != "vid1" {
		t.Fatalf("restored database: %v %+v", err, videos)
	}
	media, err := os.ReadFile(filepath.Join(root, "videos", "vid1", "original.mp4"))
	if err != nil || len(media) == 0 {
		t.Fatalf("media copy missing: %v", err)
	}
	if dir, last, lastErr := a.backupStatus(); dir == "" || last == "" || lastErr != "" {
		t.Fatalf("backup status: %q %q %q", dir, last, lastErr)
	}
}

func TestBackupFSModeAndRetention(t *testing.T) {
	a := testBackupApplication(t, "fs", 2)
	seedBackupVideo(t, a, "vid1")
	for _, old := range []string{"tiny-tube-backup-20000101-000000", "tiny-tube-backup-20000102-000000"} {
		if err := os.MkdirAll(filepath.Join(a.cfg.BackupDir, old), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(a.cfg.BackupDir, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.runBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	backups := listBackupDirs(t, a.cfg.BackupDir)
	if len(backups) != 2 {
		t.Fatalf("retention should keep 2 backups, got %v", backups)
	}
	for _, b := range backups {
		if strings.Contains(b, "20000101") {
			t.Fatalf("oldest backup not pruned: %v", backups)
		}
	}
	if _, err := os.Stat(filepath.Join(a.cfg.BackupDir, "other")); err != nil {
		t.Fatal("unrelated directory removed")
	}
	root := filepath.Join(a.cfg.BackupDir, backups[len(backups)-1])
	if _, err := os.Stat(filepath.Join(root, "videos", "vid1", "meta.json")); err != nil {
		t.Fatalf("FS metadata not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "videos", "tiny-tube.db")); !os.IsNotExist(err) {
		t.Fatal("FS backup should not contain a database copy")
	}
}

func TestBackupRequiresDir(t *testing.T) {
	a := testApplication(t)
	if err := a.runBackup(context.Background()); err == nil {
		t.Fatal("backup without BACKUP_DIR should fail")
	}
}
