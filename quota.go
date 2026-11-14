package main

import (
	"io/fs"
	"path/filepath"
)

// dirUsage sums the sizes of all files under root, including the metadata
// database when it lives inside the video directory. Symlinks are counted by
// their link size, never followed.
func dirUsage(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// quotaExceeded reports whether an upload of extra bytes would exceed the
// configured VIDEO_DIR quota. Zero quota means unlimited. The check is
// advisory under concurrency: simultaneous uploads may each pass before any
// of them lands on disk.
func (a *application) quotaExceeded(extra int64) (bool, error) {
	if a.cfg.QuotaBytes <= 0 {
		return false, nil
	}
	used, err := dirUsage(a.videoDir)
	if err != nil {
		return false, err
	}
	if extra > 0 {
		return used+extra > a.cfg.QuotaBytes, nil
	}
	return used >= a.cfg.QuotaBytes, nil
}
