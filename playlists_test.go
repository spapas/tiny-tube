package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func postAdmin(t *testing.T, h http.Handler, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPlaylistLifecycle(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			h := a.routes()
			admin := testSession(t, a, "tester")

			w := postAdmin(t, h, "/api/admin/playlists/create", url.Values{"name": {"Course"}}, admin)
			if w.Code != http.StatusSeeOther {
				t.Fatalf("create: %d %s", w.Code, w.Body.String())
			}
			id := strings.TrimPrefix(w.Header().Get("Location"), "/admin/playlists/edit?id=")
			if id == "" || strings.Contains(id, "/") {
				t.Fatalf("no playlist id in redirect: %q", w.Header().Get("Location"))
			}

			v1 := uploadWithAccessList(t, a, "First", "C", "")
			v2 := uploadWithAccessList(t, a, "Second", "C", "")
			for _, vid := range []string{v1, v2} {
				if w := postAdmin(t, h, "/api/admin/playlists/items/add", url.Values{"id": {id}, "video_id": {vid}}, admin); w.Code != http.StatusSeeOther {
					t.Fatalf("add %s: %d %s", vid, w.Code, w.Body.String())
				}
			}
			if w := postAdmin(t, h, "/api/admin/playlists/items/add", url.Values{"id": {id}, "video_id": {v1}}, admin); w.Code != http.StatusSeeOther {
				t.Fatalf("duplicate add should redirect with error, got %d", w.Code)
			} else if !strings.Contains(w.Header().Get("Location"), "error=") {
				t.Fatalf("duplicate add should report an error: %q", w.Header().Get("Location"))
			}

			page := httptest.NewRecorder()
			h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/playlist?id="+id, nil))
			if page.Code != http.StatusOK {
				t.Fatalf("playlist page: %d", page.Code)
			}
			first, second := strings.Index(page.Body.String(), v1), strings.Index(page.Body.String(), v2)
			if first < 0 || second < 0 || first > second {
				t.Fatal("playlist order wrong")
			}

			if w := postAdmin(t, h, "/api/admin/playlists/items/move", url.Values{"id": {id}, "video_id": {v1}, "direction": {"down"}}, admin); w.Code != http.StatusSeeOther {
				t.Fatalf("move: %d %s", w.Code, w.Body.String())
			}
			moved := httptest.NewRecorder()
			h.ServeHTTP(moved, httptest.NewRequest(http.MethodGet, "/playlist?id="+id, nil))
			first, second = strings.Index(moved.Body.String(), v1), strings.Index(moved.Body.String(), v2)
			if first < 0 || second < 0 || first < second {
				t.Fatal("move down did not reorder")
			}

			watch := watchAs(t, a, "/watch?id="+v1+"&playlist="+id, "10.9.0.1:1")
			if watch.Code != http.StatusOK {
				t.Fatalf("watch: %d", watch.Code)
			}
			if !strings.Contains(watch.Body.String(), "playlist="+id) || !strings.Contains(watch.Body.String(), "Second") {
				t.Fatal("watch page missing series pager")
			}

			if w := postAdmin(t, h, "/api/admin/playlists/items/remove", url.Values{"id": {id}, "video_id": {v2}}, admin); w.Code != http.StatusSeeOther {
				t.Fatalf("remove: %d", w.Code)
			}
			after := httptest.NewRecorder()
			h.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/playlist?id="+id, nil))
			if strings.Contains(after.Body.String(), v2) {
				t.Fatal("removed video still listed")
			}

			testUser(t, a, "viewer1", "viewer")
			viewer := testSession(t, a, "viewer1")
			if w := postAdmin(t, h, "/api/admin/playlists/create", url.Values{"name": {"Nope"}}, viewer); w.Code != http.StatusForbidden {
				t.Fatalf("viewer create: %d", w.Code)
			}
			if w := postAdmin(t, h, "/api/admin/playlists/create", url.Values{"name": {"Nope"}}, nil); w.Code != http.StatusSeeOther {
				t.Fatalf("anonymous create should redirect to login, got %d", w.Code)
			}

			entries, err := a.fs.ListAudit(50)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, e := range entries {
				seen[e.Action] = true
			}
			for _, action := range []string{"playlist.create", "playlist.items"} {
				if !seen[action] {
					t.Fatalf("audit missing %s", action)
				}
			}

			if w := postAdmin(t, h, "/api/admin/playlists/delete", url.Values{"id": {id}}, admin); w.Code != http.StatusSeeOther {
				t.Fatalf("delete: %d %s", w.Code, w.Body.String())
			}
			gone := httptest.NewRecorder()
			h.ServeHTTP(gone, httptest.NewRequest(http.MethodGet, "/playlist?id="+id, nil))
			if gone.Code != http.StatusNotFound {
				t.Fatalf("deleted playlist: %d", gone.Code)
			}
		})
	}
}

func TestPlaylistHidesLockedVideos(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	admin := testSession(t, a, "tester")
	w := postAdmin(t, h, "/api/admin/playlists/create", url.Values{"name": {"Mixed"}}, admin)
	id := strings.TrimPrefix(w.Header().Get("Location"), "/admin/playlists/edit?id=")
	public := uploadWithAccessList(t, a, "Public", "C", "")
	listID, _ := createLockedList(t, a, "L")
	locked := uploadWithAccessList(t, a, "Hidden", "C", listID)
	postAdmin(t, h, "/api/admin/playlists/items/add", url.Values{"id": {id}, "video_id": {public}}, admin)
	postAdmin(t, h, "/api/admin/playlists/items/add", url.Values{"id": {id}, "video_id": {locked}}, admin)

	anon := httptest.NewRecorder()
	h.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, "/playlist?id="+id, nil))
	if anon.Code != http.StatusOK {
		t.Fatalf("playlist: %d", anon.Code)
	}
	if strings.Contains(anon.Body.String(), locked) {
		t.Fatal("locked video leaks without a key")
	}
	if !strings.Contains(anon.Body.String(), public) {
		t.Fatal("public video missing")
	}

	asAdmin := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/playlist?id="+id, nil)
	r.AddCookie(admin)
	h.ServeHTTP(asAdmin, r)
	if !strings.Contains(asAdmin.Body.String(), locked) {
		t.Fatal("editor cannot manage locked playlist entry")
	}
}
