package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tiny-tube/store"
)

func testConfig(t *testing.T) config {
	t.Helper()
	return config{VideoDir: filepath.Join(t.TempDir(), "videos"), Store: "fs", Host: "127.0.0.1", Port: "8080", UploadUser: "tester", UploadPass: "test-password", MaxUploadBytes: 2 << 20, TranscodeWorkers: 2}
}

func testApplication(t *testing.T) *application {
	t.Helper()
	a, err := newApplication(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	a.ffmpegBin = "" // isolate handler tests from locally installed FFmpeg
	t.Cleanup(func() { a.close() })
	return a
}

func sampleVideo(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/sample.mp4")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func uploadRequest(t *testing.T, title, filename string, data []byte) *http.Request {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if err := w.WriteField("title", title); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("desc", "<script>alert(1)</script>"); err != nil {
		t.Fatal(err)
	}
	f, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/upload", &b)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.SetBasicAuth("tester", "test-password")
	return r
}

func TestConcurrentUploadsPreserveEveryOriginal(t *testing.T) {
	a := testApplication(t)
	handler, data := a.routes(), sampleVideo(t)
	const count = 20
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		r := uploadRequest(t, fmt.Sprintf("video-%d", i), "sample.mp4", data)
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusSeeOther {
				t.Errorf("upload: %d %s", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	items, err := a.fs.List("")
	if err != nil || len(items) != count {
		t.Fatalf("want %d uploads, got %d: %v", count, len(items), err)
	}
	titles := map[string]bool{}
	for _, m := range items {
		if titles[m.Title] {
			t.Errorf("duplicate metadata: %+v", m)
		}
		titles[m.Title] = true
		original, err := os.ReadFile(filepath.Join(a.videoDir, m.ID, m.Original))
		if err != nil || !bytes.Equal(original, data) {
			t.Fatalf("original overwritten for %s: %v", m.ID, err)
		}
		if m.Status != "ready" || m.HLS != "" {
			t.Errorf("no-FFmpeg status: %+v", m)
		}
	}
}

func TestUploadValidationAndLimits(t *testing.T) {
	data := sampleVideo(t)
	for _, tc := range []struct {
		name, title, filename string
		data                  []byte
		limit                 int64
		want                  int
		chunked               bool
	}{
		{"missing title", "", "video.mp4", data, 2 << 20, 400, false},
		{"unknown extension", "test", "video.txt", data, 2 << 20, 415, false},
		{"executable extension", "test", "video.exe", data, 2 << 20, 415, false},
		{"missing extension", "test", "video", data, 2 << 20, 415, false},
		{"fake media", "test", "video.mp4", []byte("this is not a video file"), 2 << 20, 415, false},
		{"oversized", "test", "video.mp4", data, 1024, 413, false},
		{"oversized chunked", "test", "video.mp4", data, 1024, 413, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApplication(t)
			a.cfg.MaxUploadBytes = tc.limit
			r := uploadRequest(t, tc.title, tc.filename, tc.data)
			if tc.chunked {
				r.ContentLength = -1
			}
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("want %d, got %d: %s", tc.want, w.Code, w.Body.String())
			}
			entries, err := os.ReadDir(a.videoDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected upload left data: %v, %v", entries, err)
			}
		})
	}
}

type brokenStore struct {
	store.Store
	putErr, getErr, listErr error
}

func (s brokenStore) Put(m store.Meta) error {
	if s.putErr != nil {
		return s.putErr
	}
	return s.Store.Put(m)
}
func (s brokenStore) Get(id string) (store.Meta, error) {
	if s.getErr != nil {
		return store.Meta{}, s.getErr
	}
	return s.Store.Get(id)
}
func (s brokenStore) List(q string) ([]store.Meta, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.Store.List(q)
}

func TestMetadataFailureDoesNotPublishUpload(t *testing.T) {
	a := testApplication(t)
	a.fs = brokenStore{Store: a.fs, putErr: errors.New("disk failure")}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, uploadRequest(t, "test", "video.mp4", sampleVideo(t)))
	if w.Code != 500 || w.Header().Get("Location") != "" {
		t.Fatalf("failed upload reported success: %d", w.Code)
	}
	entries, err := os.ReadDir(a.videoDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed upload left data: %v, %v", entries, err)
	}
}

func TestLargeFailedUploadCleansMultipartTemporaryFiles(t *testing.T) {
	tmp := t.TempDir()
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(key, tmp)
	}
	a := testApplication(t)
	a.cfg.MaxUploadBytes = 12 << 20
	a.fs = brokenStore{Store: a.fs, putErr: errors.New("disk failure")}
	data := append(sampleVideo(t), make([]byte, multipartMemory)...)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, uploadRequest(t, "large test", "video.mp4", data))
	if w.Code != 500 {
		t.Fatalf("want storage failure, got %d", w.Code)
	}
	files, err := os.ReadDir(tmp)
	if err != nil || len(files) != 0 {
		t.Fatalf("multipart files left behind: %v %v", files, err)
	}
	files, err = os.ReadDir(a.videoDir)
	if err != nil || len(files) != 0 {
		t.Fatalf("incomplete original left behind: %v %v", files, err)
	}
}

func TestStartupRejectsInvalidStorage(t *testing.T) {
	cfg := testConfig(t)
	if err := os.WriteFile(cfg.VideoDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newApplication(cfg); err == nil {
		t.Fatal("storage initialization failure ignored")
	}
}

func TestHTTPPlaybackAuthAndErrors(t *testing.T) {
	a := testApplication(t)
	data := sampleVideo(t)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, uploadRequest(t, "HTTP test", "video.mp4", data))
	items, err := a.fs.List("")
	if err != nil || len(items) != 1 {
		t.Fatalf("upload failed: %v %s", err, w.Body.String())
	}
	m := items[0]
	server := httptest.NewServer(a.routes())
	defer server.Close()
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/", 200}, {"/?q=HTTP", 200}, {"/watch?id=" + m.ID, 200}, {"/upload", 401}, {"/unknown", 404}, {"/watch?id=missing", 404}, {"/watch?id=..", 404}, {"/healthz", 200}, {"/readyz", 200},
	} {
		res, err := server.Client().Get(server.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("%s: want %d, got %d", tc.path, tc.want, res.StatusCode)
		}
		if strings.HasPrefix(tc.path, "/watch?id="+m.ID) && bytes.Contains(body, []byte("<script>alert(1)</script>")) {
			t.Error("unescaped description")
		}
	}
	rangeReq, _ := http.NewRequest("GET", server.URL+"/videos/"+m.ID+"/"+m.Original, nil)
	rangeReq.Header.Set("Range", "bytes=0-99")
	res, err := server.Client().Do(rangeReq)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || !bytes.Equal(part, data[:100]) || res.Header.Get("Content-Range") == "" {
		t.Fatalf("Range support broken: %d, %d bytes", res.StatusCode, len(part))
	}
	for _, path := range []string{"/upload", "/api/upload"} {
		method := "GET"
		if path == "/api/upload" {
			method = "POST"
		}
		for _, password := range []string{"wrong", "test-password"} {
			r := httptest.NewRequest(method, path, nil)
			r.SetBasicAuth("tester", password)
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, r)
			want := 401
			if password == "test-password" {
				if method == "GET" {
					want = 200
				} else {
					want = 400
				}
			}
			if w.Code != want {
				t.Errorf("auth %s: want %d got %d", path, want, w.Code)
			}
		}
	}
	a.fs = brokenStore{Store: a.fs, getErr: errors.New("storage failed"), listErr: errors.New("storage failed")}
	for _, tc := range []struct {
		path string
		want int
	}{{"/", 500}, {"/watch?id=" + m.ID, 500}, {"/readyz", 503}} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("storage failure %s: %d", tc.path, w.Code)
		}
	}
}

func TestConfigRejectsUnsafeOrInvalidSettings(t *testing.T) {
	for _, key := range []string{"UPLOAD_PASS", "UPLOAD_USER", "PORT", "HOST", "ADDR", "STORE", "DB_PATH", "MAX_UPLOAD_BYTES", "TRANSCODE_WORKERS", "FFMPEG_BIN", "AUTH_REQUESTS_PER_MINUTE", "UPLOAD_TIMEOUT_SECONDS", "TRANSCODE_TIMEOUT_SECONDS"} {
		t.Setenv(key, "")
	}
	for _, pass := range []string{"", " ", "changeme"} {
		t.Setenv("UPLOAD_PASS", pass)
		if _, err := loadConfig(); err == nil {
			t.Errorf("accepted password %q", pass)
		}
	}
	t.Setenv("UPLOAD_PASS", "test-password")
	cfg, err := loadConfig()
	if err != nil || cfg.Host != "127.0.0.1" || cfg.MaxUploadBytes != 2<<30 || cfg.TranscodeWorkers != 1 {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	for _, tc := range []struct{ key, value string }{{"PORT", "0"}, {"MAX_UPLOAD_BYTES", "-1"}, {"TRANSCODE_WORKERS", "0"}, {"UPLOAD_USER", "a:b"}, {"AUTH_REQUESTS_PER_MINUTE", "0"}, {"UPLOAD_TIMEOUT_SECONDS", "-1"}, {"TRANSCODE_TIMEOUT_SECONDS", "86401"}} {
		t.Setenv(tc.key, tc.value)
		if _, err := loadConfig(); err == nil {
			t.Errorf("accepted %s=%s", tc.key, tc.value)
		}
		t.Setenv(tc.key, "")
	}
}

func waitStatus(t *testing.T, a *application, id, status string) store.Meta {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		m, err := a.fs.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if m.Status == status {
			return m
		}
		select {
		case <-time.After(10 * time.Millisecond):
		}
	}
	m, _ := a.fs.Get(id)
	t.Fatalf("timed out waiting for %s: %+v", status, m)
	return m
}

func startTestWorkers(t *testing.T, a *application) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a.startWorkers(ctx)
	t.Cleanup(func() { cancel(); a.workers.Wait() })
	return cancel
}
