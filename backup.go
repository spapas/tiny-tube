package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tiny-tube/store"
)

// Automatic hot backups of the whole installation data. They replicate the
// manual procedure from docs/OPERATIONS.md without stopping the server:
// media files are copied live (published files are immutable) and SQLite is
// snapshotted with VACUUM INTO. Disabled when BACKUP_DIR is unset.

const backupDirPrefix = "tiny-tube-backup-"

var errBackupNoSQLite = errors.New("sqlite store unavailable for backup")

// backupManifest describes one automatic backup for future restores.
type backupManifest struct {
	Version string `json:"version"`
	Time    string `json:"time"`
	Store   string `json:"store"`
	DB      string `json:"db"`
}

func (a *application) startBackupScheduler(ctx context.Context) {
	if a.cfg.BackupDir == "" {
		return
	}
	interval := time.Duration(a.cfg.BackupIntervalHours) * time.Hour
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		// Cover fresh installations quickly, then settle on the interval.
		timer := time.NewTimer(time.Minute)
		defer timer.Stop()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		wait := timer.C
		for {
			select {
			case <-ctx.Done():
				return
			case <-wait:
				wait = ticker.C
			}
			if err := a.runBackup(ctx); err != nil {
				log.Printf("automatic backup: %v", err)
			}
		}
	}()
}

func (a *application) backupStatus() (dir, last, lastErr string) {
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	return a.cfg.BackupDir, a.lastBackup, a.lastBackupErr
}

func (a *application) setBackupStatus(last, lastErr string) {
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	a.lastBackup, a.lastBackupErr = last, lastErr
}

func (a *application) runBackup(ctx context.Context) error {
	if a.cfg.BackupDir == "" {
		return errors.New("BACKUP_DIR is not configured")
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	dest := filepath.Join(a.cfg.BackupDir, backupDirPrefix+stamp)
	fail := func(err error) error {
		a.metrics.backupFailures.inc()
		a.setBackupStatus("", err.Error())
		_ = os.RemoveAll(dest)
		return err
	}
	if err := a.copyVideoTree(ctx, dest); err != nil {
		return fail(err)
	}
	dbRel, err := a.snapshotDatabase(dest)
	if err != nil {
		return fail(err)
	}
	manifest, err := json.MarshalIndent(backupManifest{
		Version: appVersion,
		Time:    time.Now().UTC().Format(time.RFC3339Nano),
		Store:   a.cfg.Store,
		DB:      dbRel,
	}, "", "  ")
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "manifest.json"), append(manifest, '\n'), 0o644); err != nil {
		return fail(err)
	}
	if err := a.pruneBackups(); err != nil {
		return fail(err)
	}
	a.setBackupStatus(time.Now().UTC().Format(time.RFC3339Nano), "")
	a.metrics.backupLastSuccess.set(float64(time.Now().Unix()))
	log.Printf("automatic backup completed: %s", dest)
	return nil
}

// copyVideoTree replicates the video directory, skipping the live SQLite
// files (replaced by snapshotDatabase) and transient temporary files.
func (a *application) copyVideoTree(ctx context.Context, dest string) error {
	target := filepath.Join(dest, filepath.Base(a.videoDir))
	dbBase := filepath.Base(a.cfg.DBPath)
	return filepath.WalkDir(a.videoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, err := filepath.Rel(a.videoDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(target, 0o755)
		}
		// Never follow symlinks out of the media tree.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		name := d.Name()
		if !d.IsDir() {
			if a.cfg.Store != "fs" && (name == dbBase || name == dbBase+"-wal" || name == dbBase+"-shm" || name == dbBase+"-journal") {
				return nil
			}
			if strings.HasPrefix(name, ".ready-") || strings.HasPrefix(name, ".meta-") || strings.HasPrefix(name, ".audit-") || strings.HasPrefix(name, ".tmp-") || strings.HasPrefix(name, ".staging-") {
				return nil
			}
		}
		out := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		return copyBackupFile(path, out)
	})
}

func copyBackupFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	return errors.Join(copyErr, syncErr, closeErr)
}

// snapshotDatabase writes a consistent SQLite copy into the backup. FS mode
// keeps its meta.json records from the tree copy and needs no snapshot.
func (a *application) snapshotDatabase(dest string) (string, error) {
	if a.cfg.Store == "fs" {
		return "", nil
	}
	s, ok := a.fs.(*store.SQLite)
	if !ok {
		return "", errBackupNoSQLite
	}
	rel, err := filepath.Rel(a.videoDir, a.cfg.DBPath)
	var out string
	if err != nil || strings.HasPrefix(rel, "..") {
		out = filepath.Join(dest, filepath.Base(a.cfg.DBPath))
		rel = filepath.Base(a.cfg.DBPath)
	} else {
		out = filepath.Join(dest, filepath.Base(a.videoDir), rel)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	if err := s.VacuumInto(out); err != nil {
		return "", err
	}
	return rel, nil
}

func (a *application) pruneBackups() error {
	entries, err := os.ReadDir(a.cfg.BackupDir)
	if err != nil {
		return err
	}
	var backups []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), backupDirPrefix) {
			backups = append(backups, e.Name())
		}
	}
	sort.Strings(backups)
	for len(backups) > a.cfg.BackupKeep {
		oldest := backups[0]
		backups = backups[1:]
		if err := os.RemoveAll(filepath.Join(a.cfg.BackupDir, oldest)); err != nil {
			return err
		}
	}
	return nil
}
