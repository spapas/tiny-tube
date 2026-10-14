package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func jsonUploadRequest(t *testing.T, title, filename string, data []byte) *http.Request {
	t.Helper()
	r := uploadRequest(t, title, filename, data)
	r.Header.Set("Accept", "application/json")
	return r
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("invalid JSON response: %v: %q", err, w.Body.String())
	}
	return v
}

func TestAPIUploadReturnsJSON(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, jsonUploadRequest(t, "API video", "sample.mp4", sampleVideo(t)))
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	body := decodeJSON(t, w)
	id, _ := body["id"].(string)
	if id == "" || body["watch_url"] != "/watch?id="+id {
		t.Fatalf("bad upload JSON: %v", body)
	}
	if body["status"] != "ready" || body["api_url"] != "/api/video?id="+id {
		t.Fatalf("bad upload JSON fields: %v", body)
	}

	// Single-video read mirrors the upload.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/video?id="+id, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/video: %d %s", w.Code, w.Body.String())
	}
	single := decodeJSON(t, w)
	if single["id"] != id || single["title"] != "API video" {
		t.Fatalf("bad video JSON: %v", single)
	}

	// List read contains the upload.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/videos", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/videos: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Total int              `json:"total"`
		Count int              `json:"count"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("invalid list JSON: %v", err)
	}
	if list.Total != 1 || list.Count != 1 || len(list.Items) != 1 || list.Items[0]["id"] != id {
		t.Fatalf("bad list JSON: %+v", list)
	}
}

func TestAPIUploadFormatParamReturnsJSON(t *testing.T) {
	a := testApplication(t)
	r := uploadRequest(t, "Param video", "sample.mp4", sampleVideo(t))
	r.URL.RawQuery = "format=json"
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	if body := decodeJSON(t, w); body["id"] == "" {
		t.Fatalf("missing id: %v", body)
	}
}

func TestAPIBrowserUploadStillRedirects(t *testing.T) {
	a := testApplication(t)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, uploadRequest(t, "Browser video", "sample.mp4", sampleVideo(t)))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/watch?id=") {
		t.Fatalf("bad Location: %q", loc)
	}
	if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "application/json") {
		t.Fatalf("browser upload should not be JSON: %q", ct)
	}
}

func TestAPIJSONErrors(t *testing.T) {
	a := testApplication(t)
	h := a.routes()

	// Validation error as JSON.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, jsonUploadRequest(t, "", "sample.mp4", sampleVideo(t)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if body := decodeJSON(t, w); body["error"] == "" {
		t.Fatalf("missing error field: %v", body)
	}

	// Auth error as JSON.
	r := jsonUploadRequest(t, "x", "sample.mp4", sampleVideo(t))
	r.SetBasicAuth("tester", "wrong")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if body := decodeJSON(t, w); body["error"] == "" {
		t.Fatalf("missing auth error field: %v", body)
	}

	// Missing video as JSON.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/video?id=missing", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
	if body := decodeJSON(t, w); body["error"] == "" {
		t.Fatalf("missing not-found error field: %v", body)
	}
}

func TestAPIEditDeleteJSON(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, jsonUploadRequest(t, "Before", "sample.mp4", sampleVideo(t)))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	id := decodeJSON(t, w)["id"].(string)

	w, r := postForm(t, "/api/edit", url.Values{"id": {id}, "title": {"After"}, "desc": {"d"}, "category": {"c"}})
	r.SetBasicAuth("tester", "test-password")
	r.Header.Set("Accept", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	if body := decodeJSON(t, w); body["id"] != id {
		t.Fatalf("bad edit JSON: %v", body)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/video?id="+id, nil))
	if got := decodeJSON(t, w)["title"]; got != "After" {
		t.Fatalf("edit not persisted: %v", got)
	}

	w, r = postForm(t, "/api/delete", url.Values{"id": {id}})
	r.SetBasicAuth("tester", "test-password")
	r.Header.Set("Accept", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if body := decodeJSON(t, w); body["deleted"] != true {
		t.Fatalf("bad delete JSON: %v", body)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/video?id="+id, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("deleted video still visible: %d", w.Code)
	}
}
