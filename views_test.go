package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tiny-tube/store"
)

func watchAs(t *testing.T, a *application, target, remoteAddr string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.RemoteAddr = remoteAddr
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, r)
	return rec
}

func TestWatchCountsDebouncedViews(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			id := uploadWithAccessList(t, a, "Counted", "Cat", "")
			target := "/watch?id=" + id

			if rec := watchAs(t, a, target, "10.0.0.1:1234"); rec.Code != http.StatusOK {
				t.Fatalf("watch: %d", rec.Code)
			}
			if m, _ := a.fs.Get(id); m.Views != 1 {
				t.Fatalf("first view: %+v", m)
			}
			// Same viewer refresh: no new view.
			if rec := watchAs(t, a, target, "10.0.0.1:4321"); rec.Code != http.StatusOK {
				t.Fatalf("watch: %d", rec.Code)
			}
			if m, _ := a.fs.Get(id); m.Views != 1 {
				t.Fatalf("debounce failed: %+v", m)
			}
			// Another viewer: counts.
			if rec := watchAs(t, a, target, "10.0.0.2:1234"); rec.Code != http.StatusOK {
				t.Fatalf("watch: %d", rec.Code)
			}
			if m, _ := a.fs.Get(id); m.Views != 2 {
				t.Fatalf("second viewer: %+v", m)
			}
			// The displayed total follows the counter.
			if rec := watchAs(t, a, target, "10.0.0.3:1234"); !strings.Contains(rec.Body.String(), translate("el", "count.views", "Count", 3)) {
				t.Fatalf("missing view count in page: %d", rec.Code)
			}
			if rec := watchAs(t, a, "/", "10.0.0.4:1234"); !strings.Contains(rec.Body.String(), translate("el", "count.views", "Count", 3)) {
				t.Fatal("missing view count in library")
			}
			if rec := watchAs(t, a, "/api/video?id="+id, "10.0.0.5:1234"); !strings.Contains(rec.Body.String(), `"views":3`) {
				t.Fatalf("missing views in api: %s", rec.Body.String())
			}
		})
	}
}

func TestLockedWatchWithoutAccessCountsNothing(t *testing.T) {
	a := testApplication(t)
	listID, _ := createLockedList(t, a, "L")
	lockedID := uploadWithAccessList(t, a, "Hidden", "C", listID)
	if rec := watchAs(t, a, "/watch?id="+lockedID, "10.0.0.9:1234"); rec.Code != http.StatusNotFound {
		t.Fatalf("watch: %d", rec.Code)
	}
	if m, _ := a.fs.Get(lockedID); m.Views != 0 {
		t.Fatalf("denied request counted: %+v", m)
	}
}

func TestLibrarySortsByViewsOptionally(t *testing.T) {
	a := testApplication(t)
	oldID := uploadWithAccessList(t, a, "Older", "C", "")
	newID := uploadWithAccessList(t, a, "Newer", "C", "")
	for i := 0; i < 5; i++ {
		if _, err := a.fs.IncrementViews(oldID); err != nil {
			t.Fatal(err)
		}
	}
	// Default stays chronological.
	rec := watchAs(t, a, "/", "10.1.0.1:1")
	newPos, oldPos := strings.Index(rec.Body.String(), "Newer"), strings.Index(rec.Body.String(), "Older")
	if newPos < 0 || oldPos < 0 || newPos > oldPos {
		t.Fatalf("default order changed: %d %d", newPos, oldPos)
	}
	// Optional views sort.
	rec = watchAs(t, a, "/?sort=views", "10.1.0.2:1")
	newPos, oldPos = strings.Index(rec.Body.String(), "Newer"), strings.Index(rec.Body.String(), "Older")
	if newPos < 0 || oldPos < 0 || oldPos > newPos {
		t.Fatalf("views order wrong: %d %d", oldPos, newPos)
	}
	if !strings.Contains(rec.Body.String(), translate("el", "search.sortPopular")) {
		t.Fatal("missing sort control state")
	}
	// Unknown sort values fall back to recent.
	rec = watchAs(t, a, "/?sort=bogus", "10.1.0.3:1")
	if strings.Contains(rec.Body.String(), "Sort: "+translate("el", "library.sortPopularNote")) {
		t.Fatal("bogus sort accepted")
	}
	// API mirrors the sort.
	rec = watchAs(t, a, "/api/videos?sort=views", "10.1.0.4:1")
	body := rec.Body.String()
	if !strings.Contains(body, `"sort":"views"`) {
		t.Fatalf("missing sort echo: %s", body)
	}
	oldIdx, newIdx := strings.Index(body, oldID), strings.Index(body, newID)
	if oldIdx < 0 || newIdx < 0 || oldIdx > newIdx {
		t.Fatalf("api views order wrong: %s", body)
	}
	_ = newID
}

func TestWatchManageCardRespectsRoles(t *testing.T) {
	a := testApplication(t)
	id := uploadWithAccessList(t, a, "Managed", "C", "")
	target := "/watch?id=" + id

	if rec := watchAs(t, a, target, "10.2.0.1:1"); strings.Contains(rec.Body.String(), "manage-card") {
		t.Fatal("manage card leaks to anonymous")
	}
	testUser(t, a, "ed2", store.RoleEditor)
	testUser(t, a, "view2", store.RoleViewer)
	ed, viewer := testSession(t, a, "ed2"), testSession(t, a, "view2")

	if rec := watchAs(t, a, target, "10.2.0.2:1", viewer); strings.Contains(rec.Body.String(), "manage-card") {
		t.Fatal("manage card leaks to viewer")
	}
	rec := watchAs(t, a, target, "10.2.0.3:1", ed)
	if !strings.Contains(rec.Body.String(), translate("el", "video.editDetails")) {
		t.Fatal("editor lost edit button")
	}
	if strings.Contains(rec.Body.String(), translate("el", "video.delete")) {
		t.Fatal("delete leaks to editor")
	}
	rec = watchAs(t, a, target, "10.2.0.4:1", testSession(t, a, "tester"))
	if !strings.Contains(rec.Body.String(), translate("el", "video.editDetails")) || !strings.Contains(rec.Body.String(), translate("el", "video.delete")) {
		t.Fatal("admin lost management buttons")
	}
}
