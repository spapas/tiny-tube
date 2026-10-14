package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"tiny-tube/store"
)

func TestLibraryCategoryAndSearchFilters(t *testing.T) {
	a := testApplication(t)
	for _, m := range []store.Meta{
		{ID: "training", Title: "Learn Go", Category: "Εκπαίδευση & Ομάδα", Status: "ready"},
		{ID: "other", Title: "Learn Design", Category: "Παρουσιάσεις", Status: "ready"},
		{ID: "third", Title: "Go Workshop", Category: "Εκπαίδευση & Ομάδα", Status: "ready"},
	} {
		if err := a.fs.Put(m); err != nil {
			t.Fatal(err)
		}
	}
	query := url.Values{"q": {"learn"}, "category": {"Εκπαίδευση & Ομάδα"}}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/?"+query.Encode(), nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "/watch?id=training") || strings.Contains(body, "/watch?id=other") || strings.Contains(body, "/watch?id=third") {
		t.Fatal("category and search were not combined")
	}
	if !strings.Contains(body, "Παρουσιάσεις") || !strings.Contains(body, "Εκπαίδευση &amp; Ομάδα") {
		t.Fatal("filters lost their categories or escaping")
	}
	if !strings.Contains(body, `name="category" value="Εκπαίδευση &amp; Ομάδα"`) {
		t.Fatal("search does not preserve the active category")
	}
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/?q=unmatched-query", nil))
	if !strings.Contains(w.Body.String(), "Δεν βρέθηκαν βίντεο") {
		t.Fatal("missing empty search state")
	}
}

func TestEmbeddedUIAssetsAreServed(t *testing.T) {
	a := testApplication(t)
	for _, tc := range []struct{ path, contentType string }{
		{"/assets/app.css", "text/css"}, {"/assets/app.js", "javascript"},
		{"/assets/player.js", "javascript"}, {"/assets/upload.js", "javascript"},
		{"/assets/hls.min.js", "javascript"},
	} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != 200 || w.Body.Len() == 0 || !strings.Contains(w.Header().Get("Content-Type"), tc.contentType) {
			t.Errorf("asset failed: %s %d %s", tc.path, w.Code, w.Header().Get("Content-Type"))
		}
	}
}

func TestWatchPageUsesVendoredPlayer(t *testing.T) {
	a := testApplication(t)
	m := store.Meta{ID: "vendored", Title: "Watch me", Original: "original.mp4", HLS: "hls-ready/index.m3u8", Status: "ready"}
	if err := a.fs.Put(m); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/watch?id=vendored", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `/assets/hls.min.js`) {
		t.Fatal("watch page does not use the vendored player", w.Code)
	}
	if strings.Contains(body, "https://") {
		t.Fatal("watch page references an external URL")
	}
}

func TestWatchMetadataEscapesHTMLAndPlayerAttributes(t *testing.T) {
	a := testApplication(t)
	m := store.Meta{ID: "escape", Title: `<img src=x onerror="alert(1)">`, Desc: "<script>alert(1)</script>", Original: "original.mp4", HLS: "hls-safe/index.m3u8", Status: "ready"}
	if err := a.fs.Put(m); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/watch?id=escape", nil))
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, m.Title) || strings.Contains(body, m.Desc) {
		t.Fatal("watch page leaked unescaped HTML")
	}
	if !strings.Contains(body, `data-original="original.mp4"`) || !strings.Contains(body, `data-hls="hls-safe/index.m3u8"`) {
		t.Fatal("player source data was lost")
	}
}
