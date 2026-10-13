package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// transcodeToHLS: original.mp4 -> index.m3u8 + segNNN.ts (720p, single rendition v1).
// Runs in background so upload returns fast. Updates meta.json status on finish.
func transcodeToHLS(videoDir, id, original string) {
	src := filepath.Join(videoDir, id, original)
	outDir := filepath.Join(videoDir, id)
	playlist := filepath.Join(outDir, "index.m3u8")

	args := []string{
		"-y", "-i", src,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-vf", "scale=-2:720",
		"-c:a", "aac", "-b:a", "128k",
		"-hls_time", "6",
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", filepath.Join(outDir, "seg%03d.ts"),
		playlist,
	}
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = log.Writer()
	cmd.Stderr = log.Writer()
	if err := cmd.Run(); err != nil {
		log.Printf("ffmpeg failed for %s: %v", id, err)
		return
	}
	// mark ready
	m, err := fs.Get(id)
	if err != nil {
		return
	}
	m.HLS = "index.m3u8"
	m.Status = "ready"
	_ = fs.Put(m)
	log.Printf("HLS ready for %s", id)
}

func ffmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	if err == nil {
		return true
	}
	// also honour FFMPEG_BIN env for Windows custom paths
	if b := os.Getenv("FFMPEG_BIN"); b != "" {
		_, err = exec.LookPath(b)
		return err == nil
	}
	return false
}
