package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func uploadJSON(t *testing.T, h http.Handler, method, target string, body []byte, cookie *http.Cookie, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	r := httptest.NewRequest(method, target, reader)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	r.Header.Set("Accept", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func startChunkedUpload(t *testing.T, h http.Handler, admin *http.Cookie, filename string, size int64) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"filename": filename, "size": size})
	w := uploadJSON(t, h, "POST", "/api/uploads", payload, admin, "application/json")
	if w.Code != http.StatusCreated {
		t.Fatalf("init: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID        string `json:"id"`
		ChunkSize int64  `json:"chunk_size"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" || created.ChunkSize <= 0 {
		t.Fatalf("init response: %v %s", err, w.Body.String())
	}
	return created.ID
}

func TestChunkedUploadValidation(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	admin := testSession(t, a, "tester")
	payload, _ := json.Marshal(map[string]any{"filename": "x.txt", "size": 10})
	if w := uploadJSON(t, h, "POST", "/api/uploads", payload, admin, "application/json"); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("bad extension: %d", w.Code)
	}
	payload, _ = json.Marshal(map[string]any{"filename": "x.mp4", "size": 0})
	if w := uploadJSON(t, h, "POST", "/api/uploads", payload, admin, "application/json"); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("empty size: %d", w.Code)
	}
	payload, _ = json.Marshal(map[string]any{"filename": "x.mp4", "size": 1})
	if w := uploadJSON(t, h, "GET", "/api/uploads/nope", nil, admin, ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown status: %d", w.Code)
	}
	if w := uploadJSON(t, h, "POST", "/api/uploads/nope/chunk?offset=0", []byte("x"), admin, "application/octet-stream"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown chunk: %d", w.Code)
	}
}

func TestChunkedUploadRoundTrip(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	admin := testSession(t, a, "tester")
	data := sampleVideo(t)
	id := startChunkedUpload(t, h, admin, "clip.mp4", int64(len(data)))

	half := len(data) / 2
	w := uploadJSON(t, h, "POST", "/api/uploads/"+id+"/chunk?offset=0", data[:half], admin, "application/octet-stream")
	if w.Code != http.StatusOK {
		t.Fatalf("chunk 1: %d %s", w.Code, w.Body.String())
	}
	// Wrong offset reports progress without accepting bytes.
	w = uploadJSON(t, h, "POST", "/api/uploads/"+id+"/chunk?offset=0", data[:half], admin, "application/octet-stream")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"received":`) {
		t.Fatalf("offset mismatch: %d %s", w.Code, w.Body.String())
	}
	status := uploadJSON(t, h, "GET", "/api/uploads/"+id, nil, admin, "")
	if !strings.Contains(status.Body.String(), `"received":`) {
		t.Fatalf("status: %d %s", status.Code, status.Body.String())
	}
	w = uploadJSON(t, h, "POST", "/api/uploads/"+id+"/chunk?offset="+strconv.Itoa(half), data[half:], admin, "application/octet-stream")
	if w.Code != http.StatusOK {
		t.Fatalf("chunk 2: %d %s", w.Code, w.Body.String())
	}

	var b bytes.Buffer
	fw := multipart.NewWriter(&b)
	_ = fw.WriteField("title", "Chunked")
	_ = fw.WriteField("desc", "")
	_ = fw.WriteField("category", "C")
	_ = fw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/uploads/"+id+"/complete", &b)
	r.Header.Set("Content-Type", fw.FormDataContentType())
	r.Header.Set("Accept", "application/json")
	r.AddCookie(admin)
	complete := httptest.NewRecorder()
	h.ServeHTTP(complete, r)
	if complete.Code != http.StatusCreated {
		t.Fatalf("complete: %d %s", complete.Code, complete.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(complete.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("complete response: %v %s", err, complete.Body.String())
	}
	m, err := a.fs.Get(created.ID)
	if err != nil || m.Title != "Chunked" || m.Status != "ready" {
		t.Fatalf("published: %+v %v", m, err)
	}
	if _, err := os.Stat(filepath.Join(a.videoDir, created.ID, "original.mp4")); err != nil {
		t.Fatalf("original missing: %v", err)
	}
	entries, err := a.fs.ListAudit(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == auditVideoUpload && e.Target == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("chunked upload not audited")
	}
}

func TestChunkedUploadIncomplete(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	admin := testSession(t, a, "tester")
	data := sampleVideo(t)
	id := startChunkedUpload(t, h, admin, "clip.mp4", int64(len(data)))
	half := len(data) / 2
	if w := uploadJSON(t, h, "POST", "/api/uploads/"+id+"/chunk?offset=0", data[:half], admin, "application/octet-stream"); w.Code != http.StatusOK {
		t.Fatalf("chunk: %d", w.Code)
	}
	var b bytes.Buffer
	fw := multipart.NewWriter(&b)
	_ = fw.WriteField("title", "Partial")
	_ = fw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/uploads/"+id+"/complete", &b)
	r.Header.Set("Content-Type", fw.FormDataContentType())
	r.Header.Set("Accept", "application/json")
	r.AddCookie(admin)
	complete := httptest.NewRecorder()
	h.ServeHTTP(complete, r)
	if complete.Code != http.StatusBadRequest {
		t.Fatalf("incomplete complete: %d %s", complete.Code, complete.Body.String())
	}
}

func TestStaleStagingPrunedOnStartup(t *testing.T) {
	cfg := testConfig(t)
	root := cfg.VideoDir
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	old, fresh := filepath.Join(root, ".staging-old"), filepath.Join(root, ".staging-fresh")
	for _, dir := range []string{old, fresh} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	a, err := newApplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.close() })
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("stale staging kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh staging removed: %v", err)
	}
}
