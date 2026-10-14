package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tiny-tube/store"
)

func fakeHLS(ctx context.Context, bin string, args []string) error {
	playlist := args[len(args)-1]
	if err := os.WriteFile(filepath.Join(filepath.Dir(playlist), "seg000.ts"), []byte("test segment"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(playlist, []byte("#EXTM3U\n#EXTINF:1,\nseg000.ts\n#EXT-X-ENDLIST\n"), 0o644)
}

func queueVideo(t *testing.T, a *application, id string) {
	t.Helper()
	m := store.Meta{ID: id, Title: id, Original: "original.mp4", Status: "queued"}
	if err := a.fs.Put(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.videoDir, id, m.Original), sampleVideo(t), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFFmpegOverrideAndAbsence(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	bin, err := resolveFFmpeg("")
	if err != nil || bin != "" {
		t.Fatalf("absent FFmpeg: %q %v", bin, err)
	}
	bin, err = resolveFFmpeg(executable)
	if err != nil || !strings.EqualFold(bin, executable) {
		t.Fatalf("override ignored: %q %v", bin, err)
	}
	if _, err := resolveFFmpeg(filepath.Join(t.TempDir(), "missing-ffmpeg.exe")); err == nil {
		t.Fatal("invalid override silently ignored")
	}
}

func TestWorkersBoundConversionConcurrency(t *testing.T) {
	a := testApplication(t)
	a.ffmpegBin = "fake-ffmpeg"
	const jobs = 6
	for i := 0; i < jobs; i++ {
		queueVideo(t, a, string(rune('a'+i)))
	}
	started, release := make(chan struct{}, jobs), make(chan struct{}, jobs)
	a.runFFmpeg = func(ctx context.Context, bin string, args []string) error {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return fakeHLS(ctx, bin, args)
		}
	}
	startTestWorkers(t, a)
	for i := 0; i < a.cfg.TranscodeWorkers; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	items, err := a.fs.List("")
	if err != nil {
		t.Fatal(err)
	}
	processing, queued := 0, 0
	for _, m := range items {
		if m.Status == "processing" {
			processing++
		}
		if m.Status == "queued" {
			queued++
		}
	}
	if processing != 2 || queued != 4 {
		t.Fatalf("unbounded processing: processing=%d queued=%d", processing, queued)
	}
	for i := 0; i < jobs; i++ {
		release <- struct{}{}
	}
	for i := 0; i < jobs; i++ {
		m := waitStatus(t, a, string(rune('a'+i)), "ready")
		if !strings.HasPrefix(m.HLS, "hls-") {
			t.Fatalf("unpublished HLS: %+v", m)
		}
	}
}

func TestFailedConversionKeepsOriginalAndCleansStaging(t *testing.T) {
	a := testApplication(t)
	a.ffmpegBin = "fake-ffmpeg"
	queueVideo(t, a, "bad")
	a.runFFmpeg = func(ctx context.Context, bin string, args []string) error { return errors.New("encoder failed") }
	startTestWorkers(t, a)
	m := waitStatus(t, a, "bad", "failed")
	if m.Error == "" || m.HLS != "" {
		t.Fatalf("failure not visible: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(a.videoDir, m.ID, m.Original)); err != nil {
		t.Fatal("original lost", err)
	}
	dirs, _ := filepath.Glob(filepath.Join(a.videoDir, m.ID, ".hls-*"))
	if len(dirs) != 0 {
		t.Fatalf("failed staging left behind: %v", dirs)
	}
}

func TestShutdownRequeuesAndRestartResumes(t *testing.T) {
	a := testApplication(t)
	a.ffmpegBin = "fake-ffmpeg"
	queueVideo(t, a, "interrupted")
	started := make(chan struct{})
	a.runFFmpeg = func(ctx context.Context, bin string, args []string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	cancel := startTestWorkers(t, a)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("conversion did not start")
	}
	cancel()
	a.workers.Wait()
	waitStatus(t, a, "interrupted", "queued")
	cfg := a.cfg
	var err error
	cfg.FFmpegBin, err = os.Executable() // resolved override; runner is replaced below
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := newApplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	restarted.runFFmpeg = fakeHLS
	startTestWorkers(t, restarted)
	waitStatus(t, restarted, "interrupted", "ready")
}

func TestStartupReconcilesInterruptedJobs(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "no FFmpeg", true: "FFmpeg available"}[present], func(t *testing.T) {
			cfg := testConfig(t)
			if err := os.MkdirAll(cfg.VideoDir, 0o755); err != nil {
				t.Fatal(err)
			}
			fs := store.NewFS(cfg.VideoDir)
			if err := fs.Put(store.Meta{ID: "old", Original: "original.mp4", Status: "processing"}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", "")
			if present {
				var err error
				cfg.FFmpegBin, err = os.Executable()
				if err != nil {
					t.Fatal(err)
				}
			}
			a, err := newApplication(cfg)
			if err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if present {
				want = "queued"
			}
			m, err := a.fs.Get("old")
			if err != nil || m.Status != want {
				t.Fatalf("recovery: %+v %v", m, err)
			}
		})
	}
}

func TestRealFFmpegUploadAndHLS(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is not installed")
	}
	cfg := testConfig(t)
	cfg.FFmpegBin = bin
	a, err := newApplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	startTestWorkers(t, a)
	server := httptest.NewServer(a.routes())
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	request := uploadRequest(t, "real transcode", "sample.mp4", sampleVideo(t))
	request.URL, _ = request.URL.Parse(server.URL + "/api/upload")
	request.RequestURI = ""
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 303 {
		t.Fatalf("upload: %d", response.StatusCode)
	}
	items, err := a.fs.List("")
	if err != nil || len(items) != 1 {
		t.Fatalf("uploaded metadata: %v %v", items, err)
	}
	m := waitStatus(t, a, items[0].ID, "ready")
	if m.HLS == "" {
		t.Fatal("conversion did not publish HLS")
	}
	res, err := client.Get(server.URL + "/videos/" + m.ID + "/" + m.HLS)
	if err != nil {
		t.Fatal(err)
	}
	playlist, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "application/vnd.apple.mpegurl") || !strings.Contains(string(playlist), "#EXT-X-ENDLIST") {
		t.Fatalf("invalid HLS response: %d %s", res.StatusCode, playlist)
	}
	segmentURL := server.URL + "/videos/" + m.ID + "/" + filepath.ToSlash(filepath.Join(filepath.Dir(m.HLS), "seg000.ts"))
	res, err = client.Get(segmentURL)
	if err != nil {
		t.Fatal(err)
	}
	segment, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || len(segment) == 0 || !strings.Contains(res.Header.Get("Content-Type"), "video/mp2t") {
		t.Fatalf("invalid HLS segment: %d %d bytes", res.StatusCode, len(segment))
	}
}
