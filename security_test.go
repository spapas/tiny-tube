package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"tiny-tube/store"
)

func TestCrossOriginMutationsAreRejected(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testBackendApplication(t, backend)
			original := store.Meta{ID: "protected", Title: "Keep me", Status: "ready"}
			if err := a.fs.Put(original); err != nil {
				t.Fatal(err)
			}
			h := a.routes()
			for _, path := range []string{"/api/upload", "/api/edit", "/api/delete"} {
				for _, tc := range []struct{ name, origin, site string }{
					{"foreign origin", "https://attacker.example", ""},
					{"cross site", "", "cross-site"},
					{"same site different origin", "", "same-site"},
					{"opaque origin", "null", ""},
				} {
					t.Run(path+"/"+tc.name, func(t *testing.T) {
						w, r := postForm(t, path, url.Values{"id": {"protected"}, "title": {"Changed"}})
						r.AddCookie(testSession(t, a, "tester"))
						r.Header.Set("Origin", tc.origin)
						r.Header.Set("Sec-Fetch-Site", tc.site)
						h.ServeHTTP(w, r)
						if w.Code != 403 {
							t.Fatalf("cross-origin mutation accepted: %d %s", w.Code, w.Body.String())
						}
						m, err := a.fs.Get("protected")
						if err != nil || m.Title != original.Title {
							t.Fatalf("cross-origin mutation changed metadata: %+v %v", m, err)
						}
					})
				}
			}
		})
	}
}

func TestDatabaseHardLinkCannotBeServedAsVideo(t *testing.T) {
	a := testBackendApplication(t, "sqlite")
	dir := filepath.Join(a.videoDir, "alias")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a.cfg.DBPath, filepath.Join(dir, "original.mp4")); err != nil {
		t.Fatal("could not create temporary DB alias", err)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/videos/alias/original.mp4", nil))
	if w.Code != 404 {
		t.Fatalf("database alias was exposed: %d (%d bytes)", w.Code, w.Body.Len())
	}
}

func TestFSModeProtectsLeftoverSQLiteAliases(t *testing.T) {
	a := testApplication(t)
	if err := os.WriteFile(filepath.Join(a.videoDir, "tiny-tube.db"), []byte("leftover private metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := a.fs.Dir("leftover")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(a.videoDir, "tiny-tube.db"), filepath.Join(dir, "original.mp4")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/videos/leftover/original.mp4", nil))
	if w.Code != 404 {
		t.Fatal("FS mode exposed a leftover SQLite alias", w.Code)
	}
}

func TestSameOriginAndCLIJSONClientsStillWork(t *testing.T) {
	for _, site := range []string{"same-origin", ""} {
		t.Run(site, func(t *testing.T) {
			a := testApplication(t)
			r := jsonUploadRequest(t, a, "Allowed", "sample.mp4", sampleVideo(t))
			if site != "" {
				r.Header.Set("Sec-Fetch-Site", site)
				r.Header.Set("Origin", "http://example.com")
			}
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, r)
			if w.Code != 201 {
				t.Fatalf("legitimate JSON upload blocked: %d %s", w.Code, w.Body.String())
			}
			if decodeJSON(t, w)["id"] == "" {
				t.Fatal("JSON upload lost ID")
			}
		})
	}
	a := testApplication(t)
	w, r := postForm(t, "/api/edit", url.Values{"id": {"missing"}, "title": {"test"}})
	r.Header.Set("Origin", "https://other.example")
	r.Header.Set("Accept", "application/json")
	r.AddCookie(testSession(t, a, "tester"))
	a.routes().ServeHTTP(w, r)
	if w.Code != 403 || decodeJSON(t, w)["error"] == "" {
		t.Fatal("CSRF error did not preserve JSON API")
	}
}

func TestAuthLimitCannotBeBypassedWithSessionOrForwardingHeaders(t *testing.T) {
	a := testApplication(t)
	a.authRequests = newAuthLimiter(3)
	now := time.Now()
	a.authRequests.now = func() time.Time { return now }
	h := a.routes()
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest("GET", "/upload", nil)
		r.RemoteAddr = fmt.Sprintf("192.0.2.1:%d", 1000+i)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 303 {
			t.Fatal("anonymous upload page", w.Code)
		}
	}
	// A valid session from the throttled IP is still limited.
	for _, user := range []string{"", "tester"} {
		w, r := postForm(t, "/api/edit", url.Values{"id": {"missing"}, "title": {"x"}})
		r.RemoteAddr = "192.0.2.1:9999"
		if user != "" {
			r.AddCookie(testSession(t, a, user))
		}
		r.Header.Set("X-Forwarded-For", "203.0.113.50")
		r.Header.Set("Forwarded", "for=203.0.113.51")
		r.Header.Set("Accept", "application/json")
		h.ServeHTTP(w, r)
		if w.Code != 429 || w.Header().Get("Retry-After") != "60" || decodeJSON(t, w)["error"] == "" {
			t.Fatalf("auth throttling bypassed: %d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/videos", nil))
	if w.Code != 200 {
		t.Fatal("public reads throttled", w.Code)
	}
	now = now.Add(time.Minute)
	r := httptest.NewRequest("GET", "/upload", nil)
	r.RemoteAddr = "192.0.2.1:9999"
	r.AddCookie(testSession(t, a, "tester"))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("auth did not recover after cooldown", w.Code)
	}
}

func TestAuthLimiterHasBoundedClientMemory(t *testing.T) {
	l := newAuthLimiter(1)
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < maxAuthClients; i++ {
		if ok, _ := l.allow(fmt.Sprintf("client-%d", i)); !ok {
			t.Fatal("unexpected rejection")
		}
	}
	if ok, _ := l.allow("overflow-one"); !ok {
		t.Fatal("overflow bucket did not allow first request")
	}
	if ok, _ := l.allow("overflow-two"); ok {
		t.Fatal("unique clients bypassed overflow limit")
	}
	if len(l.clients) != maxAuthClients {
		t.Fatal("unbounded auth client memory", len(l.clients))
	}
	now = now.Add(time.Minute)
	if ok, _ := l.allow("new-client"); !ok || len(l.clients) != 1 {
		t.Fatal("expired clients not reclaimed")
	}
}

func TestSecurityHeadersApplyToPagesAssetsJSONAndErrors(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	for _, path := range []string{"/", "/upload", "/assets/app.js", "/assets/theme.js", "/api/videos", "/not-found"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Referrer-Policy") != "same-origin" {
			t.Fatal("missing response protections", path, w.Header())
		}
		csp := w.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "form-action 'self'") || strings.Contains(csp, "'unsafe-inline'") || strings.Contains(csp, "'unsafe-eval'") {
			t.Fatal("unsafe CSP", csp)
		}
	}
}

func TestPublicMediaRejectsPrivateFilesAndUnsafePaths(t *testing.T) {
	a := testApplication(t)
	a.cfg.DBPath = filepath.Join(t.TempDir(), "external.db")
	if err := os.MkdirAll(a.fs.Dir("check"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := a.routes()
	for i, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		private := a.cfg.DBPath + suffix
		if err := os.WriteFile(private, []byte("private database data"), 0o600); err != nil {
			t.Fatal(err)
		}
		alias := fmt.Sprintf("alias-%d.jpg", i)
		if err := os.Link(private, filepath.Join(a.fs.Dir("check"), alias)); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/videos/check/"+alias, nil))
		if w.Code != 404 {
			t.Fatal("private file alias exposed", suffix, w.Code)
		}
	}
	if err := os.WriteFile(filepath.Join(a.fs.Dir("check"), ".meta-private.json"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Metadata JSON is not media: a non-dot .json file must not be public.
	if err := os.WriteFile(filepath.Join(a.fs.Dir("check"), "meta.json"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(a.fs.Dir("check"), "directory.mp4"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/videos/check/.meta-private.json", "/videos/check/meta.json", "/videos/check/directory.mp4", "/videos/check/sub%5c..%5c..%5cprivate.mp4", "/videos/check/original.mp4:private", "/videos/check/staging/.hls/data.ts"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("unsafe media path %s: %d", path, w.Code)
		}
	}
}

func TestMediaCannotFollowLinkOutsideStorage(t *testing.T) {
	a := testApplication(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private.mp4"), []byte("must not be public"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(a.videoDir, "linked")
	if runtime.GOOS == "windows" {
		// Directory junctions need no elevation, unlike Windows symbolic links.
		ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
		cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", "New-Item -ItemType Junction -Path "+quote(link)+" -Target "+quote(outside)+" -ErrorAction Stop | Out-Null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("create temporary junction: %v %s", err, out)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(link)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/videos/linked/private.mp4", nil))
	if w.Code != 404 {
		t.Fatalf("external link exposed storage: %d %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(outside, "private.mp4"))
	if err != nil || string(data) != "must not be public" {
		t.Fatal("outside file changed", err)
	}
}

func TestConversionTimeoutFailsWithoutRequeueLoop(t *testing.T) {
	a := testApplication(t)
	a.cfg.TranscodeTimeoutSeconds = 1
	a.ffmpegBin = "fake"
	queueVideo(t, a, "timeout")
	a.runFFmpeg = func(ctx context.Context, _ string, _ []string) error { <-ctx.Done(); return ctx.Err() }
	startTestWorkers(t, a)
	m := waitStatus(t, a, "timeout", "failed")
	if !strings.Contains(m.Error, "TRANSCODE_TIMEOUT_SECONDS") || m.HLS != "" {
		t.Fatal("timeout not reported", m)
	}
	if _, err := os.Stat(filepath.Join(a.fs.Dir(m.ID), m.Original)); err != nil {
		t.Fatal("timeout lost original", err)
	}
	if dirs, _ := filepath.Glob(filepath.Join(a.fs.Dir(m.ID), ".hls-*")); len(dirs) != 0 {
		t.Fatal("timeout left staging", dirs)
	}
	if _, found, err := a.claimJob(context.Background()); err != nil || found {
		t.Fatal("timed-out job requeued", found, err)
	}
}

func TestInvalidConversionPathsDoNotInvokeFFmpeg(t *testing.T) {
	a := testApplication(t)
	called := false
	a.runFFmpeg = func(context.Context, string, []string) error { called = true; return nil }
	for _, name := range []string{"../private.mp4", "original.mp4:secret", "playlist.m3u8", "CON", "https://other.example/clip.mp4"} {
		if _, err := a.transcodeToHLS(context.Background(), store.Meta{ID: "test", Original: name}); err == nil {
			t.Fatal("unsafe original accepted", name)
		}
	}
	if called {
		t.Fatal("FFmpeg invoked for unsafe original")
	}
}

func TestSlowUploadIsTerminatedAndCleaned(t *testing.T) {
	a := testApplication(t)
	a.cfg.UploadTimeoutSeconds = 1
	srv := a.httpServer()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); srv.Serve(listener) }()
	defer func() { srv.Close(); <-done }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	cookie := testSession(t, a, "tester")
	_, err = fmt.Fprintf(conn, "POST /api/upload HTTP/1.1\r\nHost: %s\r\nCookie: %s=%s\r\nContent-Type: multipart/form-data; boundary=slow\r\nContent-Length: 500\r\nConnection: close\r\n\r\n--slow\r\n", listener.Addr().String(), cookie.Name, cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), httptest.NewRequest("POST", "/api/upload", nil))
	if err != nil {
		t.Fatal("stalled upload did not return in time", err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	if response.StatusCode != 408 {
		t.Fatal("stalled upload not terminated", response.StatusCode)
	}
	assertNoVideoDirs(t, a.videoDir)
}

func TestStartupRejectsUnpatchedGoBuilds(t *testing.T) {
	for _, version := range []string{"go1.26.0", "go1.26.8", "go1.27.1", "devel", "unknown"} {
		if err := requirePatchedGo(version); err == nil {
			t.Error("accepted unpatched or unverified build", version)
		}
	}
	for _, version := range []string{"go1.26.9", "go1.27.2", "go1.28.0"} {
		if err := requirePatchedGo(version); err != nil {
			t.Error("rejected patched build", version, err)
		}
	}
}

func TestFFmpegLauncherTargetsRealExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(t.TempDir(), "ffmpeg.exe")
	marker := strings.TrimSuffix(launcher, ".exe") + ".shim"
	if err := os.WriteFile(marker, []byte("path = \""+executable+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := unwrapFFmpegShim(launcher)
	if err != nil || got != executable {
		t.Fatal("launcher not resolved", got, err)
	}
	if err := os.WriteFile(marker, []byte("path = \""+executable+"\"\nargs = \"custom arguments\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := unwrapFFmpegShim(launcher); err == nil {
		t.Fatal("unsupported wrapper arguments ignored")
	}
	if err := os.WriteFile(marker, []byte("path = \"missing-relative.exe\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := unwrapFFmpegShim(launcher); err == nil {
		t.Fatal("unsafe wrapper target accepted")
	}
}

func TestSQLInjectionAndStoredHTMLRemainData(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testBackendApplication(t, backend)
			m := store.Meta{ID: "untrusted", Title: "'); DROP TABLE videos; --", Desc: "<script>alert(1)</script>", Category: "<img src=x onerror=alert(1)>", Status: "ready"}
			if err := a.fs.Put(m); err != nil {
				t.Fatal(err)
			}
			h := a.routes()
			for _, path := range []string{"/", "/watch?id=untrusted", "/edit?id=untrusted"} {
				w := httptest.NewRecorder()
				r := httptest.NewRequest("GET", path, nil)
				r.AddCookie(testSession(t, a, "tester"))
				h.ServeHTTP(w, r)
				if w.Code != 200 || strings.Contains(w.Body.String(), m.Desc) || strings.Contains(w.Body.String(), m.Category) {
					t.Fatalf("stored HTML executed or page failed: %s %d", path, w.Code)
				}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/api/video?id=untrusted", nil))
			if w.Code != 200 || decodeJSON(t, w)["title"] != m.Title {
				t.Fatal("SQL input did not remain data", w.Code)
			}
			if items, err := a.fs.List(""); err != nil || len(items) != 1 {
				t.Fatal("metadata damaged by SQL input", items, err)
			}
		})
	}
}
