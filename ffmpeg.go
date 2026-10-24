package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"tiny-tube/store"
)

// An explicit FFMPEG_BIN takes precedence; an invalid override is a startup error.
func resolveFFmpeg(override string) (string, error) {
	name := override
	if name == "" {
		name = "ffmpeg"
	}
	bin, err := exec.LookPath(name)
	if err != nil {
		if override == "" {
			return "", nil
		}
		return "", fmt.Errorf("FFMPEG_BIN: %w", err)
	}
	if runtime.GOOS == "windows" {
		bin, err = unwrapFFmpegShim(bin)
		if err != nil {
			return "", err
		}
	}
	return filepath.Abs(bin)
}

// Scoop's launcher is a separate process. Resolve its metadata to the real
// executable so CommandContext cancellation targets FFmpeg rather than its launcher.
func unwrapFFmpegShim(bin string) (string, error) {
	for attempt := 0; attempt < 4; attempt++ {
		marker := strings.TrimSuffix(bin, filepath.Ext(bin)) + ".shim"
		data, err := os.ReadFile(marker)
		if os.IsNotExist(err) {
			return bin, nil
		}
		if err != nil {
			return "", fmt.Errorf("read FFmpeg launcher: %w", err)
		}
		target := ""
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), "\"")
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "path":
				target = value
			case "args":
				if value != "" {
					return "", fmt.Errorf("FFmpeg launcher arguments are unsupported; set FFMPEG_BIN to the real executable")
				}
			}
		}
		if !filepath.IsAbs(target) {
			return "", fmt.Errorf("FFmpeg launcher has no absolute executable path")
		}
		bin, err = exec.LookPath(target)
		if err != nil {
			return "", fmt.Errorf("FFmpeg launcher target: %w", err)
		}
	}
	return "", fmt.Errorf("FFmpeg launcher chain is too long or cyclic")
}

func (a *application) recoverJobs() error {
	items, err := store.PendingVideos(a.fs, true)
	if err != nil {
		return err
	}
	for _, m := range items {
		if m.Status != "processing" && m.Status != "queued" {
			continue
		}
		m.HLS, m.Error = "", ""
		if a.ffmpegBin == "" {
			m.Status, m.Error = "failed", "FFmpeg is unavailable; original playback is still available."
		} else {
			m.Status = "queued"
		}
		if err = a.putMeta(m); err != nil {
			return fmt.Errorf("recover %s: %w", m.ID, err)
		}
	}
	return nil
}

func (a *application) notifyWorkers() {
	for i := 0; i < a.cfg.TranscodeWorkers; i++ {
		select {
		case a.wake <- struct{}{}:
		default:
		}
	}
}

func (a *application) startWorkers(ctx context.Context) {
	if a.ffmpegBin == "" {
		return
	}
	for i := 0; i < a.cfg.TranscodeWorkers; i++ {
		a.workers.Add(1)
		go func() {
			defer a.workers.Done()
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-a.wake:
				case <-ticker.C:
				}
				for ctx.Err() == nil {
					m, found, err := a.claimJob(ctx)
					if err != nil {
						log.Printf("claim conversion: %v", err)
						break
					}
					if !found {
						break
					}
					a.claimMu.Lock()
					job := a.activeJobs[m.ID]
					a.claimMu.Unlock()
					a.transcode(job.ctx, m)
				}
			}
		}()
	}
	a.notifyWorkers()
}

func (a *application) claimJob(ctx context.Context) (store.Meta, bool, error) {
	a.claimMu.Lock()
	defer a.claimMu.Unlock()
	items, err := store.PendingVideos(a.fs, false)
	if err != nil {
		return store.Meta{}, false, err
	}
	// List is newest first; service the oldest queued upload first.
	for i := len(items) - 1; i >= 0; i-- {
		m := items[i]
		if m.Status != "queued" || a.deleting[m.ID] {
			continue
		}
		m.Status, m.Error = "processing", ""
		if err := a.putMeta(m); err != nil {
			return m, false, err
		}
		jobCtx, cancel := context.WithTimeout(ctx, time.Duration(a.cfg.TranscodeTimeoutSeconds)*time.Second)
		a.activeJobs[m.ID] = &conversionJob{ctx: jobCtx, cancel: cancel, done: make(chan struct{})}
		return m, true, nil
	}
	return store.Meta{}, false, nil
}

func (a *application) transcode(ctx context.Context, m store.Meta) {
	hls, err := a.transcodeToHLS(ctx, m)
	a.claimMu.Lock()
	defer a.claimMu.Unlock()
	defer func() {
		if job := a.activeJobs[m.ID]; job != nil {
			job.cancel()
			delete(a.activeJobs, m.ID)
			close(job.done)
		}
	}()
	if a.deleting[m.ID] {
		if hls != "" {
			a.removePublishedHLS(m.ID, hls)
		}
		return
	}
	// Conversion only owns playback/status fields. Preserve edits made while running.
	current, getErr := a.fs.Get(m.ID)
	if getErr != nil {
		log.Printf("read conversion status %s: %v", m.ID, getErr)
		if hls != "" {
			a.removePublishedHLS(m.ID, hls)
		}
		return
	}
	m = current
	m.HLS, m.Error = "", ""
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		m.Status, m.Error = "failed", "Conversion exceeded TRANSCODE_TIMEOUT_SECONDS; original playback is still available."
	case ctx.Err() != nil:
		m.Status = "queued" // graceful shutdown; persisted work resumes next startup
	case err != nil:
		log.Printf("ffmpeg failed for %s: %v", m.ID, err)
		m.Status, m.Error = "failed", "HLS conversion failed. Check the server log; original playback is still available."
	default:
		m.Status, m.HLS = "ready", hls
		poster := filepath.ToSlash(filepath.Join(filepath.Dir(filepath.FromSlash(hls)), "poster.jpg"))
		if info, statErr := os.Stat(filepath.Join(a.videoDir, m.ID, filepath.FromSlash(poster))); statErr == nil && info.Size() > 0 {
			m.Poster = poster
		}
	}
	if putErr := a.putMeta(m); putErr != nil {
		log.Printf("save conversion status %s: %v", m.ID, putErr)
		if hls != "" {
			a.removePublishedHLS(m.ID, hls)
		}
	} else if m.Status != "ready" && hls != "" {
		a.removePublishedHLS(m.ID, hls)
	}
}

func (a *application) removePublishedHLS(id, hls string) {
	// hls comes from transcodeToHLS, which allocated this output under this ID.
	dir := filepath.Join(a.videoDir, id, filepath.Dir(filepath.FromSlash(hls)))
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("unpublished HLS cleanup %s: %v", id, err)
	}
}

func (a *application) transcodeToHLS(ctx context.Context, m store.Meta) (string, error) {
	format, err := originalFormat(m.Original)
	if !store.ValidID(m.ID) || err != nil {
		return "", fmt.Errorf("invalid conversion path")
	}
	outDir := filepath.Join(a.videoDir, m.ID)
	tmp, err := os.MkdirTemp(outDir, ".hls-")
	if err != nil {
		return "", err
	}
	// Only our newly allocated staging directory is removed here.
	defer func() {
		if err := os.RemoveAll(tmp); err != nil {
			log.Printf("HLS staging cleanup: %v", err)
		}
	}()
	args := []string{
		"-nostdin", "-y", "-protocol_whitelist", "file", "-f", format, "-i", filepath.Join(outDir, m.Original),
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-vf", "scale=-2:720",
		"-c:a", "aac", "-b:a", "128k", "-hls_time", "6", "-hls_playlist_type", "vod",
		"-hls_segment_filename", filepath.Join(tmp, "seg%03d.ts"), filepath.Join(tmp, "index.m3u8"),
	}
	if err = a.runFFmpeg(ctx, a.ffmpegBin, args); err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	playlist, err := os.ReadFile(filepath.Join(tmp, "index.m3u8"))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(string(playlist), "#EXTM3U") || !strings.Contains(string(playlist), "#EXT-X-ENDLIST") {
		return "", fmt.Errorf("FFmpeg did not produce a complete VOD playlist")
	}
	// Posters share the bounded conversion worker; a missing frame is non-fatal.
	a.extractPoster(ctx, filepath.Join(outDir, m.Original), filepath.Join(tmp, "poster.jpg"))
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	published := strings.TrimPrefix(filepath.Base(tmp), ".")
	if err = os.Rename(tmp, filepath.Join(outDir, published)); err != nil {
		return "", err
	}
	return published + "/index.m3u8", nil
}

func (a *application) extractPoster(ctx context.Context, src, dst string) {
	format, err := originalFormat(filepath.Base(src))
	if err != nil {
		return
	}
	for _, seek := range []bool{true, false} {
		args := []string{"-nostdin", "-y", "-protocol_whitelist", "file", "-f", format}
		if seek {
			args = append(args, "-ss", "1")
		}
		args = append(args, "-i", src, "-frames:v", "1", "-vf", "scale=640:-2", "-q:v", "3", dst)
		err := a.runFFmpeg(ctx, a.ffmpegBin, args)
		if ctx.Err() != nil {
			return
		}
		if info, statErr := os.Stat(dst); err == nil && statErr == nil && info.Size() > 0 {
			return
		}
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		log.Printf("poster cleanup: %v", err)
	}
	log.Printf("poster unavailable for %s", src)
}

func originalFormat(name string) (string, error) {
	switch name {
	case "original.mp4", "original.mov":
		return "mov", nil
	case "original.mkv", "original.webm":
		return "matroska", nil
	}
	return "", fmt.Errorf("invalid original filename")
}
