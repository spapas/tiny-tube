package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tiny-tube/store"
)

func TestAdminConversionsPage(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	admin := testSession(t, a, "tester")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := a.fs.Put(store.Meta{ID: "conv1", Title: "Queued One", Status: "queued", Created: now}); err != nil {
		t.Fatal(err)
	}
	if err := a.fs.Put(store.Meta{ID: "conv2", Title: "Failed One", Status: "failed", Error: "boom", Created: now}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/admin/conversions", nil)
	r.AddCookie(admin)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("conversions page: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		translate("el", "admin.conversions"),
		"Queued One", "Failed One", "boom",
		translate("el", "status.queued"), translate("el", "status.failed"),
		`action="/api/transcode/retry"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("conversions page missing %q", want)
		}
	}

	anon := httptest.NewRecorder()
	h.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, "/admin/conversions", nil))
	if anon.Code != http.StatusSeeOther {
		t.Fatalf("anonymous conversions page: %d", anon.Code)
	}

	testUser(t, a, "viewer1", "viewer")
	viewerRec := httptest.NewRecorder()
	viewerReq := httptest.NewRequest(http.MethodGet, "/admin/conversions", nil)
	viewerReq.AddCookie(testSession(t, a, "viewer1"))
	h.ServeHTTP(viewerRec, viewerReq)
	if viewerRec.Code != http.StatusForbidden {
		t.Fatalf("viewer conversions page: %d", viewerRec.Code)
	}
}
