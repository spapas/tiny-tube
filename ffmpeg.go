package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	if ctx.Err() != nil {
		return store.Meta{}, false, nil
	}
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
		if ctx.Err() != nil {
			return store.Meta{}, false, nil
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
		a.metrics.transcodes.inc("discarded")
		if hls != "" {
			a.removePublishedHLS(m.ID, hls)
		}
		return
	}
	// Conversion only owns playback/status fields. Preserve edits made while running.
	current, getErr := a.fs.Get(m.ID)
	if getErr != nil {
		log.Printf("read conversion status %s: %v", m.ID, getErr)
		a.metrics.transcodes.inc("failed")
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
	a.metrics.transcodes.inc(transcodeOutcome(m.Status))
	if putErr := a.putMeta(m); putErr != nil {
		log.Printf("save conversion status %s: %v", m.ID, putErr)
		if hls != "" {
			a.removePublishedHLS(m.ID, hls)
		}
	} else if m.Status != "ready" && hls != "" {
		a.removePublishedHLS(m.ID, hls)
	}
}

// transcodeOutcome maps a persisted status to a metric label. "queued" here
// means the job was canceled and resumes after restart.
func transcodeOutcome(status string) string {
	switch status {
	case "ready", "failed":
		return status
	}
	return "canceled"
}

func (a *application) removePublishedHLS(id, hls string) {
	// hls comes from transcodeToHLS, which allocated this output under this ID.
	dir := filepath.Join(a.videoDir, id, filepath.Dir(filepath.FromSlash(hls)))
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("unpublished HLS cleanup %s: %v", id, err)
	}
}

func (a *application) transcodeToHLS(ctx context.Context, m store.Meta) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	format, err := originalFormat(m.Original)
	if !store.ValidID(m.ID) || err != nil {
		return "", fmt.Errorf("invalid conversion path")
	}
	outDir := filepath.Join(a.videoDir, m.ID)
	// Faststart the original in place so direct downloads seek immediately.
	// Failures are non-fatal: the original stays playable as uploaded.
	a.faststartOriginal(ctx, outDir, m.Original, format)
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
	}
	for _, r := range hlsRenditions {
		if err := os.MkdirAll(filepath.Join(tmp, r.name), 0o755); err != nil {
			return "", err
		}
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
			"-vf", fmt.Sprintf("scale=-2:'min(%d,ih)'", r.height),
			"-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "128k",
			"-hls_time", "6", "-hls_playlist_type", "vod",
			"-hls_segment_filename", filepath.Join(tmp, r.name, "seg%03d.ts"),
			filepath.Join(tmp, r.name, "index.m3u8"),
		)
	}
	if err = a.runFFmpeg(ctx, a.ffmpegBin, args); err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err := writeHLSMaster(tmp); err != nil {
		return "", err
	}
	// Posters share the bounded conversion worker; a missing frame is non-fatal.
	a.extractPoster(ctx, filepath.Join(outDir, m.Original), filepath.Join(tmp, "poster.jpg"))
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	// Seek previews share the worker too; missing storyboards are non-fatal.
	a.extractThumbnails(ctx, filepath.Join(outDir, m.Original), tmp)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	published := strings.TrimPrefix(filepath.Base(tmp), ".")
	if err = os.Rename(tmp, filepath.Join(outDir, published)); err != nil {
		return "", err
	}
	return published + "/master.m3u8", nil
}

// hlsRenditions lists the published renditions, smallest first. Sources
// smaller than a rendition are kept at their own height (never upscaled).
var hlsRenditions = []struct {
	name   string
	height int
}{
	{"480p", 480},
	{"720p", 720},
	{"1080p", 1080},
}

// writeHLSMaster validates every rendition playlist FFmpeg produced and
// writes the variant master playlist players use for automatic quality
// selection. Bandwidths are measured from the actual segments so adaptive
// switching follows the real bitrates.
func writeHLSMaster(tmp string) error {
	var master strings.Builder
	master.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
	for _, r := range hlsRenditions {
		data, err := os.ReadFile(filepath.Join(tmp, r.name, "index.m3u8"))
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(data), "#EXTM3U") || !strings.Contains(string(data), "#EXT-X-ENDLIST") {
			return fmt.Errorf("FFmpeg did not produce a complete VOD playlist for %s", r.name)
		}
		bandwidth, err := renditionBandwidth(tmp, r.name, string(data))
		if err != nil {
			return err
		}
		fmt.Fprintf(&master, "#EXT-X-STREAM-INF:BANDWIDTH=%d\n%s/index.m3u8\n", bandwidth, r.name)
	}
	return os.WriteFile(filepath.Join(tmp, "master.m3u8"), []byte(master.String()), 0o644)
}

func renditionBandwidth(tmp, name, playlist string) (int64, error) {
	var seconds float64
	var total int64
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "#EXTINF:"); ok {
			duration, _, _ := strings.Cut(rest, ",")
			d, err := strconv.ParseFloat(strings.TrimSpace(duration), 64)
			if err != nil || d <= 0 {
				return 0, fmt.Errorf("unparseable segment duration in %s playlist", name)
			}
			seconds += d
		} else if line != "" && !strings.HasPrefix(line, "#") {
			if strings.ContainsAny(line, `/\`) {
				return 0, fmt.Errorf("unexpected segment reference in %s playlist", name)
			}
			info, err := os.Stat(filepath.Join(tmp, name, line))
			if err != nil || info.Size() <= 0 {
				return 0, fmt.Errorf("missing segment in %s playlist", name)
			}
			total += info.Size()
		}
	}
	if seconds <= 0 || total <= 0 {
		return 0, fmt.Errorf("empty %s playlist", name)
	}
	if bandwidth := int64(float64(total*8) / seconds); bandwidth > 0 {
		return bandwidth, nil
	}
	return 1, nil
}

// faststartOriginal remuxes MP4/MOV originals with the moov atom first so
// direct downloads start and seek without fetching the whole file. It runs
// inside the bounded conversion worker and shares its cancellation; any
// failure keeps the uploaded original untouched.
func (a *application) faststartOriginal(ctx context.Context, outDir, original, format string) {
	if format != "mov" || a.ffmpegBin == "" {
		return
	}
	src := filepath.Join(outDir, original)
	tmp, err := os.CreateTemp(outDir, ".faststart-*.mp4")
	if err != nil {
		log.Printf("faststart staging %s: %v", original, err)
		return
	}
	name := tmp.Name()
	tmp.Close()
	defer func() {
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
			log.Printf("faststart cleanup: %v", err)
		}
	}()
	args := []string{"-nostdin", "-y", "-protocol_whitelist", "file", "-f", format, "-i", src,
		"-c", "copy", "-movflags", "faststart", name}
	if err := a.runFFmpeg(ctx, a.ffmpegBin, args); err != nil {
		log.Printf("faststart %s: %v", original, err)
		return
	}
	if ctx.Err() != nil {
		return
	}
	info, err := os.Stat(name)
	if err != nil || info.Size() == 0 {
		if err != nil {
			log.Printf("faststart %s: %v", original, err)
		}
		return
	}
	if err := os.Rename(name, src); err != nil {
		log.Printf("faststart publish %s: %v", original, err)
	}
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
		args = append(args, "-i", src, "-frames:v", "1", "-vf", "scale=640:-2", "-q:v", "3", "-update", "1", dst)
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

// extractThumbnails builds seek-preview storyboard sheets (10x10 tiles of
// 160x90 frames, one frame every ten seconds) and a WebVTT index pointing
// at each thumbnail. Like posters it shares the bounded conversion worker;
// any failure keeps HLS and the poster intact.
func (a *application) extractThumbnails(ctx context.Context, src, tmp string) {
	if a.ffmpegBin == "" {
		return
	}
	format, err := originalFormat(filepath.Base(src))
	if err != nil {
		return
	}
	duration, err := playlistDuration(filepath.Join(tmp, "480p", "index.m3u8"))
	if err != nil || duration <= 0 {
		log.Printf("storyboard duration %s: %v", src, err)
		return
	}
	thumbs := int(math.Ceil(duration / 10))
	if thumbs < 1 {
		return
	}
	args := []string{"-nostdin", "-y", "-protocol_whitelist", "file", "-f", format, "-i", src,
		"-vf", "fps=1/10,scale=160:90:force_original_aspect_ratio=decrease,pad=160:90:(ow-iw)/2:(oh-ih)/2,tile=10x10",
		"-q:v", "4", filepath.Join(tmp, "sprite-%d.jpg")}
	if err := a.runFFmpeg(ctx, a.ffmpegBin, args); err != nil {
		log.Printf("storyboard %s: %v", src, err)
		return
	}
	if ctx.Err() != nil {
		return
	}
	sheets, err := filepath.Glob(filepath.Join(tmp, "sprite-*.jpg"))
	if err != nil || len(sheets) == 0 {
		log.Printf("storyboard unavailable for %s", src)
		return
	}
	if max := len(sheets) * 100; thumbs > max {
		thumbs = max
	}
	var vtt strings.Builder
	vtt.WriteString("WEBVTT\n")
	for i := 0; i < thumbs; i++ {
		fmt.Fprintf(&vtt, "\n%s --> %s\nsprite-%d.jpg#xywh=%d,%d,160,90\n",
			formatVTTTime(float64(i*10)), formatVTTTime(float64((i+1)*10)),
			i/100+1, (i%100)%10*160, (i%100)/10*90)
	}
	if err := os.WriteFile(filepath.Join(tmp, "thumbnails.vtt"), []byte(vtt.String()), 0o644); err != nil {
		log.Printf("storyboard index %s: %v", src, err)
	}
}

// playlistDuration sums EXTINF durations so storyboards can size their cue
// sheets without probing the original.
func playlistDuration(path string) (float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var total float64
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "#EXTINF:")
		if !ok {
			continue
		}
		duration, _, _ := strings.Cut(rest, ",")
		d, err := strconv.ParseFloat(strings.TrimSpace(duration), 64)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("unparseable segment duration")
		}
		total += d
	}
	if total <= 0 {
		return 0, fmt.Errorf("empty playlist")
	}
	return total, nil
}

func formatVTTTime(seconds float64) string {
	total := int(math.Round(seconds))
	return fmt.Sprintf("%02d:%02d:%02d.%03d", total/3600, (total/60)%60, total%60, int(math.Round((seconds-math.Floor(seconds))*1000))%1000)
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
