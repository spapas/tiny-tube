package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	if filepath.Ext(playlist) == ".jpg" {
		return os.WriteFile(playlist, []byte("fake poster"), 0o644)
	}
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

func TestRetryTranscodeReusesOriginalAndPreservesMetadata(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			listID, _ := createLockedList(t, a, "Training")
			failedID := uploadWithAccessList(t, a, "Keep title", "Training", listID)
			originalID := uploadWithAccessList(t, a, "Original only", "", "")
			m, err := a.fs.Get(failedID)
			if err != nil {
				t.Fatal(err)
			}
			m.Status, m.Error = "failed", "previous failure"
			if err := a.putMeta(m); err != nil {
				t.Fatal(err)
			}
			editor := testSession(t, a, "tester")
			testUser(t, a, "reader", "viewer")
			viewer := testSession(t, a, "reader")
			post := func(id string, cookie *http.Cookie, origin string, want int) {
				t.Helper()
				r := httptest.NewRequest("POST", "/api/transcode/retry", strings.NewReader(url.Values{"id": {id}}.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("Accept", "application/json")
				r.Header.Set("Origin", origin)
				if cookie != nil {
					r.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				a.routes().ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("retry: %d %s, want %d", w.Code, w.Body.String(), want)
				}
			}
			post(failedID, nil, "", 401)
			post(failedID, viewer, "", 403)
			post(failedID, editor, "https://other.example", 403)
			post(failedID, editor, "", 503)
			a.ffmpegBin, a.runFFmpeg = "fake", fakeHLS
			watch := serve(t, a, "GET", "/watch?id="+failedID, editor)
			if !strings.Contains(watch.Body.String(), `action="/api/transcode/retry"`) {
				t.Fatal("retry action missing from the editor's watch page")
			}
			post(failedID, editor, "", 202)
			post(failedID, editor, "", 409)
			post(originalID, editor, "", 202)
			startTestWorkers(t, a)
			ready := waitStatus(t, a, failedID, "ready")
			if ready.Title != m.Title || ready.Category != m.Category || ready.AccessList != listID || ready.Error != "" || ready.HLS == "" || ready.Poster == "" {
				t.Fatalf("retry discarded metadata or did not publish HLS: %+v", ready)
			}
			waitStatus(t, a, originalID, "ready")
			post(failedID, editor, "", 409)
			if w := serve(t, a, "GET", "/videos/"+failedID+"/"+ready.HLS); w.Code != 404 {
				t.Fatalf("retry changed media visibility: %d", w.Code)
			}
		})
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
		if filepath.Ext(args[len(args)-1]) == ".jpg" {
			return fakeHLS(ctx, bin, args)
		}
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
	ctx, stop := context.WithCancel(context.Background())
	stop()
	if _, found, err := a.claimJob(ctx); found || err != nil {
		t.Fatalf("canceled worker claimed requeued conversion: found=%v err=%v", found, err)
	}
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
	t.Cleanup(func() { restarted.close() })
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
			t.Cleanup(func() { a.close() })
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
	t.Cleanup(func() { a.close() })
	startTestWorkers(t, a)
	h := a.routes()
	request := uploadRequest(t, a, "real transcode", "sample.mp4", sampleVideo(t))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 303 {
		t.Fatalf("upload: %d", response.Code)
	}
	items, err := a.fs.List("")
	if err != nil || len(items) != 1 {
		t.Fatalf("uploaded metadata: %v %v", items, err)
	}
	m := waitStatus(t, a, items[0].ID, "ready")
	if m.HLS == "" {
		t.Fatal("conversion did not publish HLS")
	}
	if m.Poster == "" {
		t.Fatal("conversion did not publish a poster")
	}
	base := "/videos/" + m.ID + "/"
	poster := serve(t, a, "GET", base+m.Poster)
	if poster.Code != 200 || poster.Body.Len() == 0 || !strings.Contains(poster.Header().Get("Content-Type"), "image/jpeg") {
		t.Fatalf("invalid poster: %d %d bytes", poster.Code, poster.Body.Len())
	}
	playlist := serve(t, a, "GET", base+m.HLS)
	if playlist.Code != 200 || !strings.Contains(playlist.Header().Get("Content-Type"), "application/vnd.apple.mpegurl") || !strings.Contains(playlist.Body.String(), "#EXT-X-ENDLIST") {
		t.Fatalf("invalid HLS response: %d %s", playlist.Code, playlist.Body.String())
	}
	segmentName := filepath.Join(filepath.Dir(m.HLS), "seg000.ts")
	segment := serve(t, a, "GET", base+filepath.ToSlash(segmentName))
	if segment.Code != 200 || segment.Body.Len() == 0 || !strings.Contains(segment.Header().Get("Content-Type"), "video/mp2t") {
		t.Fatalf("invalid HLS segment: %d %d bytes", segment.Code, segment.Body.Len())
	}
	// The fixture is H.264 4:4:4. Verify the generated stream uses the
	// widely supported 4:2:0 pixel format even with that input.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, a.ffmpegBin, "-nostdin", "-i", filepath.Join(a.videoDir, m.ID, segmentName),
		"-vf", "showinfo", "-frames:v", "1", "-f", "null", "-").CombinedOutput()
	if err != nil || !strings.Contains(string(output), "fmt:yuv420p ") {
		t.Fatalf("HLS segment pixel format: %v\n%s", err, output)
	}
}
