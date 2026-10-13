package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

func TestUploadAuth(t *testing.T) {
	t.Setenv("UPLOAD_USER", "admin")
	t.Setenv("UPLOAD_PASS", "secret")
	ok := uploadAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("next"))
	}))

	// no creds -> 401 + challenge
	r := httptest.NewRequest("GET", "/upload", nil)
	w := httptest.NewRecorder()
	ok.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no creds = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), `Basic realm="upload"`) {
		t.Errorf("missing WWW-Authenticate header: %v", w.Header())
	}

	// wrong pass -> 401
	r = httptest.NewRequest("GET", "/upload", nil)
	r.SetBasicAuth("admin", "wrong")
	w = httptest.NewRecorder()
	ok.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong pass = %d, want 401", w.Code)
	}

	// right creds -> next
	r = httptest.NewRequest("GET", "/upload", nil)
	r.SetBasicAuth("admin", "secret")
	w = httptest.NewRecorder()
	ok.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "next" {
		t.Errorf("right creds = %d %q, want 200 next", w.Code, w.Body.String())
	}
}

func setupTestFS(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	videoDir = dir
	fs = store.NewFS(dir)
	return dir
}

func TestHandleWatchOK(t *testing.T) {
	setupTestFS(t)
	_ = fs.Put(store.Meta{ID: "w1", Title: "Watch me", Desc: "d", Category: "C", Original: "original.mp4", HLS: "index.m3u8", Status: "ready"})
	r := httptest.NewRequest("GET", "/watch?id=w1", nil)
	w := httptest.NewRecorder()
	handleWatch(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("watch = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, s := range []string{"speedbar", "playbackRate", "DOMContentLoaded", `data-s="0.5"`, `data-s="2"`, "Watch me"} {
		if !strings.Contains(body, s) {
			t.Errorf("watch page missing %q", s)
		}
	}
}

func TestHandleWatchNotFound(t *testing.T) {
	setupTestFS(t)
	r := httptest.NewRequest("GET", "/watch?id=nope", nil)
	w := httptest.NewRecorder()
	handleWatch(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("watch missing = %d, want 404", w.Code)
	}
}

func TestHandleWatchInvalidID(t *testing.T) {
	setupTestFS(t)
	r := httptest.NewRequest("GET", "/watch?id=../evil", nil)
	w := httptest.NewRecorder()
	handleWatch(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("watch invalid id = %d, want 404", w.Code)
	}
}

func TestHandleListEmpty(t *testing.T) {
	setupTestFS(t)
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handleList(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("list = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("list Content-Type = %q, want html+utf-8", ct)
	}
	if !strings.Contains(w.Body.String(), "Κανένα βίντεο") {
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
	setupTestFS(t)
	r := multipartUpload(t, map[string]string{"title": "   "}, "a.mp4", []byte("x"))
	w := httptest.NewRecorder()
	handleUpload(w, r)
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
		{"evil.exe", "original.mp4"},
		{"noext", "original.mp4"},
	}
	for _, tc := range cases {
		dir := setupTestFS(t)
		r := multipartUpload(t, map[string]string{"title": "T"}, tc.filename, []byte("fake-video-bytes"))
		w := httptest.NewRecorder()
		handleUpload(w, r)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("%s: status = %d, want 303", tc.filename, w.Code)
		}
		loc := w.Header().Get("Location")
		if !strings.HasPrefix(loc, "/watch?id=") {
			t.Fatalf("%s: Location = %q", tc.filename, loc)
		}
		id := strings.TrimPrefix(loc, "/watch?id=")
		m, err := fs.Get(id)
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
		if m.Status != "ready" && m.Status != "processing" {
			t.Errorf("%s: Status = %q, want ready|processing", tc.filename, m.Status)
		}
	}
}

func TestHandleListPoster(t *testing.T) {
	setupTestFS(t)
	_ = fs.Put(store.Meta{ID: "withp", Title: "With Poster", Created: "2025-01-01T00:00:00Z", Poster: "poster.jpg", Status: "ready"})
	_ = fs.Put(store.Meta{ID: "nop", Title: "No Poster", Created: "2024-01-01T00:00:00Z", Status: "ready"})
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handleList(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<img class="thumb" loading="lazy" src="/videos/withp/poster.jpg"`) {
		t.Error("list should render poster img for videos with Poster")
	}
	if !strings.Contains(body, `thumb ph`) {
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
