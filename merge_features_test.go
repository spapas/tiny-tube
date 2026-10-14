package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tiny-tube/store"
)

func TestGetenv(t *testing.T) {
	t.Setenv("TINY_TUBE_TESTING_XYZ", "v")
	if getenv("TINY_TUBE_TESTING_XYZ", "def") != "v" {
		t.Error("getenv should return env value")
	}
	_ = os.Unsetenv("TINY_TUBE_TESTING_DEFXYZ")
	if getenv("TINY_TUBE_TESTING_DEFXYZ", "def") != "def" {
		t.Error("getenv should return default when unset")
	}
}

func TestUploadRequiresEditorRole(t *testing.T) {
	a := testApplication(t)
	testUser(t, a, "viewer1", store.RoleViewer)
	testUser(t, a, "editor1", store.RoleEditor)
	h := a.routes()

	// Anonymous browsers are sent to login; JSON clients get 401.
	r := httptest.NewRequest("GET", "/upload", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=") {
		t.Errorf("anonymous browser = %d %q, want redirect to login", w.Code, w.Header().Get("Location"))
	}
	r = httptest.NewRequest("POST", "/api/upload", nil)
	r.Header.Set("Accept", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous JSON = %d, want 401", w.Code)
	}

	// Viewers cannot upload; editors and admins can reach the page.
	for _, tc := range []struct {
		user string
		want int
	}{
		{"viewer1", http.StatusForbidden},
		{"editor1", http.StatusOK},
		{"tester", http.StatusOK},
	} {
		r = httptest.NewRequest("GET", "/upload", nil)
		r.AddCookie(testSession(t, a, tc.user))
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s = %d, want %d", tc.user, w.Code, tc.want)
		}
	}
}

func setupTestFS(t *testing.T) *application {
	t.Helper()
	return testApplication(t)
}

func TestHandleWatchOK(t *testing.T) {
	a := setupTestFS(t)
	_ = a.fs.Put(store.Meta{ID: "w1", Title: "Watch me", Desc: "d", Category: "C", Original: "original.mp4", HLS: "index.m3u8", Status: "ready"})
	r := httptest.NewRequest("GET", "/watch?id=w1", nil)
	w := httptest.NewRecorder()
	a.handleWatch(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("watch = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, s := range []string{"speedbar", "/assets/player.js", `data-speed="0.5"`, `data-speed="2"`, "Watch me"} {
		if !strings.Contains(body, s) {
			t.Errorf("watch page missing %q", s)
		}
	}
	if strings.Contains(body, "autoplay") || !strings.Contains(body, `<video id="v" controls playsinline`) {
		t.Error("watch page should start with sound, without autoplay/muted attributes")
	}
	if strings.Contains(body, `notice notice-info`) {
		t.Error("ready video with HLS should not show the transcode hint")
	}
	// processing video without HLS keeps the hint
	_ = a.fs.Put(store.Meta{ID: "w2", Title: "Wait", Original: "original.mp4", Status: "processing"})
	r2 := httptest.NewRequest("GET", "/watch?id=w2", nil)
	w2 := httptest.NewRecorder()
	a.handleWatch(w2, r2)
	if !strings.Contains(w2.Body.String(), `notice notice-info`) {
		t.Error("processing video should still show the transcode hint")
	}
}

func postForm(t *testing.T, target string, v url.Values) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	r := httptest.NewRequest("POST", target, strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return httptest.NewRecorder(), r
}

func TestHandleEdit(t *testing.T) {
	a := setupTestFS(t)
	_ = a.fs.Put(store.Meta{ID: "e1", Title: "Old", Desc: "d", Category: "C", Created: "2024-01-01T00:00:00Z", Original: "original.mp4", HLS: "index.m3u8", Poster: "poster.jpg", Status: "ready"})

	// edit page renders with current values
	r := httptest.NewRequest("GET", "/edit?id=e1", nil)
	w := httptest.NewRecorder()
	a.handleEditPage(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `value="Old"`) {
		t.Errorf("edit page = %d, want 200 with prefilled title", w.Code)
	}
	r404 := httptest.NewRequest("GET", "/edit?id=nope", nil)
	w404 := httptest.NewRecorder()
	a.handleEditPage(w404, r404)
	if w404.Code != http.StatusNotFound {
		t.Errorf("edit page missing = %d, want 404", w404.Code)
	}

	// update title/desc/category, rest preserved
	w2, r2 := postForm(t, "/api/edit", url.Values{"id": {"e1"}, "title": {"New"}, "desc": {"nd"}, "category": {"NC"}})
	a.handleEdit(w2, r2)
	if w2.Code != http.StatusSeeOther {
		t.Fatalf("edit = %d, want 303", w2.Code)
	}
	got, _ := a.fs.Get("e1")
	if got.Title != "New" || got.Desc != "nd" || got.Category != "NC" {
		t.Errorf("edit did not apply: %+v", got)
	}
	if got.Original != "original.mp4" || got.HLS != "index.m3u8" || got.Poster != "poster.jpg" || got.Status != "ready" || got.Created != "2024-01-01T00:00:00Z" {
		t.Errorf("edit must preserve other fields: %+v", got)
	}

	// empty title rejected, unknown id 404
	w3, r3 := postForm(t, "/api/edit", url.Values{"id": {"e1"}, "title": {"  "}})
	a.handleEdit(w3, r3)
	if w3.Code != http.StatusBadRequest {
		t.Errorf("empty title = %d, want 400", w3.Code)
	}
	w4, r4 := postForm(t, "/api/edit", url.Values{"id": {"nope"}, "title": {"x"}})
	a.handleEdit(w4, r4)
	if w4.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", w4.Code)
	}
}

func TestHandleDelete(t *testing.T) {
	a := setupTestFS(t)
	dir := a.videoDir
	_ = os.MkdirAll(filepath.Join(dir, "d1"), 0o755)
	_ = a.fs.Put(store.Meta{ID: "d1", Title: "Gone", Original: "original.mp4", Status: "ready"})
	w, r := postForm(t, "/api/delete", url.Values{"id": {"d1"}})
	a.handleDelete(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("delete = %d loc %q, want 303 /", w.Code, w.Header().Get("Location"))
	}
	if _, err := a.fs.Get("d1"); err == nil {
		t.Error("deleted video should be gone")
	}
	w2, r2 := postForm(t, "/api/delete", url.Values{})
	a.handleDelete(w2, r2)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("missing id = %d, want 400", w2.Code)
	}
	w3, r3 := postForm(t, "/api/delete", url.Values{"id": {"nope"}})
	a.handleDelete(w3, r3)
	if w3.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", w3.Code)
	}
}

func TestHandleWatchAdminButtons(t *testing.T) {
	a := setupTestFS(t)
	_ = a.fs.Put(store.Meta{ID: "a1", Title: "Adm", Status: "ready"})
	r := httptest.NewRequest("GET", "/watch?id=a1", nil)
	w := httptest.NewRecorder()
	a.handleWatch(w, r)
	body := w.Body.String()
	if !strings.Contains(body, `href="/edit?id=a1"`) || !strings.Contains(body, `action="/api/delete"`) {
		t.Error("watch page should link edit + delete actions")
	}
}

func TestHandleWatchNotFound(t *testing.T) {
	a := setupTestFS(t)
	r := httptest.NewRequest("GET", "/watch?id=nope", nil)
	w := httptest.NewRecorder()
	a.handleWatch(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("watch missing = %d, want 404", w.Code)
	}
}

func TestHandleWatchInvalidID(t *testing.T) {
	a := setupTestFS(t)
	r := httptest.NewRequest("GET", "/watch?id=../evil", nil)
	w := httptest.NewRecorder()
	a.handleWatch(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("watch invalid id = %d, want 404", w.Code)
	}
}

func TestHandleListEmpty(t *testing.T) {
	a := setupTestFS(t)
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	a.handleList(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("list = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("list Content-Type = %q, want html+utf-8", ct)
	}
	if !strings.Contains(w.Body.String(), "Δεν υπάρχουν ακόμη βίντεο") {
		t.Error("empty list should show Greek empty message")
	}
}

func multipartUpload(t *testing.T, fields map[string]string, filename string, content []byte) *http.Request {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/upload", &b)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func TestHandleUploadTitleRequired(t *testing.T) {
	a := setupTestFS(t)
	r := multipartUpload(t, map[string]string{"title": "   "}, "a.mp4", []byte("x"))
	w := httptest.NewRecorder()
	a.handleUpload(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty title = %d, want 400", w.Code)
	}
}

func TestHandleUploadExtWhitelist(t *testing.T) {
	cases := []struct {
		filename string
		wantOrig string
	}{
		{"clip.mp4", "original.mp4"},
		{"clip.MKV", "original.mkv"},
		{"clip.mov", "original.mov"},
		{"clip.webm", "original.webm"},
	}
	for _, tc := range cases {
		a := setupTestFS(t)
		dir := a.videoDir
		data := sampleVideo(t)
		if tc.wantOrig == "original.mkv" || tc.wantOrig == "original.webm" {
			data = []byte{0x1a, 0x45, 0xdf, 0xa3, 0, 0, 0, 0, 0, 0, 0, 0}
		}
		r := multipartUpload(t, map[string]string{"title": "T"}, tc.filename, data)
		w := httptest.NewRecorder()
		a.handleUpload(w, r)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("%s: status = %d, want 303", tc.filename, w.Code)
		}
		loc := w.Header().Get("Location")
		if !strings.HasPrefix(loc, "/watch?id=") {
			t.Fatalf("%s: Location = %q", tc.filename, loc)
		}
		id := strings.TrimPrefix(loc, "/watch?id=")
		m, err := a.fs.Get(id)
		if err != nil {
			t.Fatalf("%s: Get(%s): %v", tc.filename, id, err)
		}
		if m.Original != tc.wantOrig {
			t.Errorf("%s: Original = %q, want %q", tc.filename, m.Original, tc.wantOrig)
		}
		if _, err := os.Stat(filepath.Join(dir, id, tc.wantOrig)); err != nil {
			t.Errorf("%s: original file missing: %v", tc.filename, err)
		}
		// status depends on ffmpeg presence: ready (no ffmpeg) or processing (ffmpeg found, async HLS)
		if m.Status != "ready" && m.Status != "queued" {
			t.Errorf("%s: Status = %q, want ready|processing", tc.filename, m.Status)
		}
	}
}

func TestHandleListPoster(t *testing.T) {
	a := setupTestFS(t)
	_ = a.fs.Put(store.Meta{ID: "withp", Title: "With Poster", Created: "2025-01-01T00:00:00Z", Poster: "poster.jpg", Status: "ready"})
	_ = a.fs.Put(store.Meta{ID: "nop", Title: "No Poster", Created: "2024-01-01T00:00:00Z", Status: "ready"})
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	a.handleList(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<img class="video-poster" loading="lazy" src="/videos/withp/poster.jpg"`) {
		t.Error("list should render poster img for videos with Poster")
	}
	if !strings.Contains(body, `video-cover`) {
		t.Error("list should render gradient placeholder for videos without Poster")
	}
}

func TestParseDotEnv(t *testing.T) {
	got := parseDotEnv("# comment\n\nVIDEO_DIR=videos\nADDR = 127.0.0.1:8081 \n" +
		"QUOTED=\"a b\"\nSINGLE='x'\nEMPTY=\nexport FOO=bar\nnoequals\n=noname\n")
	want := map[string]string{
		"VIDEO_DIR": "videos",
		"ADDR":      "127.0.0.1:8081",
		"QUOTED":    "a b",
		"SINGLE":    "x",
		"EMPTY":     "",
		"FOO":       "bar",
	}
	if len(got) != len(want) {
		t.Fatalf("parse = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("parse[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	content := "TINY_TUBE_DOTENV_A=fromfile\nTINY_TUBE_DOTENV_B=fromfile\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINY_TUBE_DOTENV_B", "fromenv")
	t.Setenv("TINY_TUBE_DOTENV_A", "")
	_ = os.Unsetenv("TINY_TUBE_DOTENV_A")
	loadDotEnv(p)
	if got := os.Getenv("TINY_TUBE_DOTENV_A"); got != "fromfile" {
		t.Errorf("A = %q, want fromfile", got)
	}
	if got := os.Getenv("TINY_TUBE_DOTENV_B"); got != "fromenv" {
		t.Errorf("B = %q, want fromenv (explicit env must win)", got)
	}
	// missing file = no-op, must not crash
	loadDotEnv(filepath.Join(dir, "nope.env"))
}
