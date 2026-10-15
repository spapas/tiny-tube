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
	return config{VideoDir: filepath.Join(t.TempDir(), "videos"), Store: "fs", Host: "127.0.0.1", Port: "8080", AdminUser: "tester", AdminPass: "test-password", MaxUploadBytes: 2 << 20, TranscodeWorkers: 2}
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

func uploadRequest(t *testing.T, a *application, title, filename string, data []byte) *http.Request {
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
	r.AddCookie(testSession(t, a, "tester"))
	return r
}

// assertNoVideoDirs fails if the video directory holds anything besides the
// .auth user/session records.
func assertNoVideoDirs(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == ".auth" {
			continue
		}
		t.Fatalf("leftover data: %v", entries)
	}
}

// testUser creates an active local user with the given role.
func testUser(t *testing.T, a *application, username, role string) {
	t.Helper()
	hash, err := hashPassword("test-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.fs.PutUser(store.User{
		Username: username, DisplayName: username, PasswordHash: hash,
		AuthSource: store.AuthLocal, Role: role, Active: true,
	}); err != nil {
		t.Fatal(err)
	}
}

// testSession issues a real session for an existing user and returns its cookie.
func testSession(t *testing.T, a *application, username string) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	if err := a.issueSession(w, username); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("no session cookie issued")
	return nil
}

// loginRequest performs a real credential login and returns the session cookie.
func loginRequest(t *testing.T, h http.Handler, username, password string) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("login for %q: %d %s", username, w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("login issued no session cookie")
	return nil
}

func TestConcurrentUploadsPreserveEveryOriginal(t *testing.T) {
	a := testApplication(t)
	handler, data := a.routes(), sampleVideo(t)
	const count = 20
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		r := uploadRequest(t, a, fmt.Sprintf("video-%d", i), "sample.mp4", data)
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
			r := uploadRequest(t, a, tc.title, tc.filename, tc.data)
			if tc.chunked {
				r.ContentLength = -1
			}
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("want %d, got %d: %s", tc.want, w.Code, w.Body.String())
			}
			assertNoVideoDirs(t, a.videoDir)
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
	a.routes().ServeHTTP(w, uploadRequest(t, a, "test", "video.mp4", sampleVideo(t)))
	if w.Code != 500 || w.Header().Get("Location") != "" {
		t.Fatalf("failed upload reported success: %d", w.Code)
	}
	assertNoVideoDirs(t, a.videoDir)
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
	a.routes().ServeHTTP(w, uploadRequest(t, a, "large test", "video.mp4", data))
	if w.Code != 500 {
		t.Fatalf("want storage failure, got %d", w.Code)
	}
	files, err := os.ReadDir(tmp)
	if err != nil || len(files) != 0 {
		t.Fatalf("multipart files left behind: %v %v", files, err)
	}
	assertNoVideoDirs(t, a.videoDir)
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
	a.routes().ServeHTTP(w, uploadRequest(t, a, "HTTP test", "video.mp4", data))
	items, err := a.fs.List("")
	if err != nil || len(items) != 1 {
		t.Fatalf("upload failed: %v %s", err, w.Body.String())
	}
	m := items[0]
	server := httptest.NewServer(a.routes())
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/", 200}, {"/?q=HTTP", 200}, {"/watch?id=" + m.ID, 200}, {"/upload", 303}, {"/unknown", 404}, {"/watch?id=missing", 404}, {"/watch?id=..", 404}, {"/healthz", 200}, {"/readyz", 200}, {"/login", 200}, {"/admin", 303}, {"/admin/users", 303}, {"/admin/users/new", 303}, {"/admin/keys", 303},
	} {
		res, err := client.Get(server.URL + tc.path)
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
	for _, tc := range []struct {
		path, user string
		method     string
		want       int
	}{
		{"/upload", "", "GET", 303},
		{"/api/upload", "", "POST", 401},
		{"/upload", "viewer1", "GET", 403},
		{"/upload", "tester", "GET", 200},
		{"/api/upload", "tester", "POST", 400},
	} {
		testUser(t, a, "viewer1", store.RoleViewer)
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.path == "/api/upload" {
			r.Header.Set("Accept", "application/json")
		}
		if tc.user != "" {
			r.AddCookie(testSession(t, a, tc.user))
		}
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("auth %s as %q: want %d got %d", tc.path, tc.user, tc.want, w.Code)
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
	for _, key := range []string{"ADMIN_USER", "ADMIN_PASS", "AUTH_MODE", "PORT", "HOST", "ADDR", "STORE", "DB_PATH", "MAX_UPLOAD_BYTES", "TRANSCODE_WORKERS", "FFMPEG_BIN", "AUTH_REQUESTS_PER_MINUTE", "UPLOAD_TIMEOUT_SECONDS", "TRANSCODE_TIMEOUT_SECONDS"} {
		t.Setenv(key, "")
	}
	cfg, err := loadConfig()
	if err != nil || cfg.Host != "127.0.0.1" || cfg.MaxUploadBytes != 2<<30 || cfg.TranscodeWorkers != 1 || cfg.AuthMode != "local" || cfg.AdminUser != "admin" {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	for _, tc := range []struct{ key, value string }{{"PORT", "0"}, {"MAX_UPLOAD_BYTES", "-1"}, {"TRANSCODE_WORKERS", "0"}, {"AUTH_MODE", "sso"}, {"AUTH_REQUESTS_PER_MINUTE", "0"}, {"UPLOAD_TIMEOUT_SECONDS", "-1"}, {"TRANSCODE_TIMEOUT_SECONDS", "86401"}} {
		t.Setenv(tc.key, tc.value)
		if _, err := loadConfig(); err == nil {
			t.Errorf("accepted %s=%s", tc.key, tc.value)
		}
		t.Setenv(tc.key, "")
	}
}

func TestFirstBootSeedsAdminAndRejectsWeakPassword(t *testing.T) {
	for _, pass := range []string{"", " ", "changeme"} {
		cfg := testConfig(t)
		cfg.AdminPass = pass
		if _, err := newApplication(cfg); err == nil {
			t.Errorf("fresh boot accepted password %q", pass)
		}
	}
	cfg := testConfig(t)
	cfg.AdminUser = "not valid!"
	if _, err := newApplication(cfg); err == nil {
		t.Error("fresh boot accepted invalid admin username")
	}
	a, err := newApplication(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	u, err := a.fs.GetUser("tester")
	if err != nil || !u.IsAdmin() || !u.Active || u.AuthSource != store.AuthLocal {
		t.Fatalf("seeded admin: %+v %v", u, err)
	}
	if u.PasswordHash == "" || u.PasswordHash == "test-password" {
		t.Error("seeded admin password not hashed")
	}
	// A second boot against the same directory keeps working; ADMIN_PASS is
	// only consulted when no users exist.
	b, err := newApplication(configForDir(t, a.videoDir))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
}

func configForDir(t *testing.T, dir string) config {
	t.Helper()
	cfg := testConfig(t)
	cfg.VideoDir = dir
	return cfg
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
