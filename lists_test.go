package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"tiny-tube/store"
)

func testApplicationWithStore(t *testing.T, backend string) *application {
	t.Helper()
	cfg := testConfig(t)
	cfg.Store = backend
	a, err := newApplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.ffmpegBin = ""
	t.Cleanup(func() { a.close() })
	return a
}

// createLockedList mints a list directly in storage and returns its raw key.
func createLockedList(t *testing.T, a *application, name string) (id, raw string) {
	t.Helper()
	var err error
	id, err = store.GenerateAccessListID()
	if err != nil {
		t.Fatal(err)
	}
	var prefix, hash string
	raw, prefix, hash, err = store.GenerateAccessKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.fs.PutAccessList(store.AccessList{ID: id, Name: name, Prefix: prefix, KeyHash: hash}); err != nil {
		t.Fatal(err)
	}
	return id, raw
}

func uploadWithAccessList(t *testing.T, a *application, title, category, listID string) string {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if err := w.WriteField("title", title); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("category", category); err != nil {
		t.Fatal(err)
	}
	if listID != "" {
		if err := w.WriteField("access_list", listID); err != nil {
			t.Fatal(err)
		}
	}
	f, err := w.CreateFormFile("file", "sample.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(sampleVideo(t)); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/upload", &b)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("Accept", "application/json")
	r.AddCookie(testSession(t, a, "tester"))
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload %q: %d %s", title, rec.Code, rec.Body.String())
	}
	// Resolve the new video by listing.
	all, err := a.fs.List("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.Title == title {
			return m.ID
		}
	}
	t.Fatalf("uploaded video %q not found", title)
	return ""
}

func serve(t *testing.T, a *application, method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, r)
	return rec
}

func grantCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == listGrantCookieName {
			return c
		}
	}
	t.Fatal("no unlock cookie granted")
	return nil
}

func TestLockedListFlow(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			listID, raw := createLockedList(t, a, "Έλεγχοι")
			publicID := uploadWithAccessList(t, a, "Public training", "Training", "")
			lockedID := uploadWithAccessList(t, a, "Secret audit", "Audits", listID)
			locked, err := a.fs.Get(lockedID)
			if err != nil || locked.AccessList != listID {
				t.Fatalf("locked assignment: %+v %v", locked, err)
			}
			mediaPath := "/videos/" + lockedID + "/" + locked.Original

			// Anonymous: locked video is invisible everywhere.
			if rec := serve(t, a, "GET", "/"); strings.Contains(rec.Body.String(), "Secret audit") {
				t.Fatal("locked title leaks in library")
			} else if !strings.Contains(rec.Body.String(), "Public training") {
				t.Fatal("public video missing from library")
			}
			if rec := serve(t, a, "GET", "/api/videos"); !strings.Contains(rec.Body.String(), `"total":1`) {
				t.Fatalf("api total leaks locked video: %s", rec.Body.String())
			}
			if rec := serve(t, a, "GET", "/api/video?id="+lockedID); rec.Code != http.StatusNotFound {
				t.Fatalf("api single: %d", rec.Code)
			}
			if rec := serve(t, a, "GET", "/watch?id="+lockedID); rec.Code != http.StatusNotFound {
				t.Fatalf("watch: %d", rec.Code)
			}
			if rec := serve(t, a, "GET", mediaPath); rec.Code != http.StatusNotFound {
				t.Fatalf("media: %d", rec.Code)
			}
			if rec := serve(t, a, "GET", "/?q=Secret"); strings.Contains(rec.Body.String(), "Secret audit") {
				t.Fatal("locked title leaks via search")
			}
			if rec := serve(t, a, "GET", "/api/videos?q=Secret"); !strings.Contains(rec.Body.String(), `"count":0`) {
				t.Fatalf("api search leaks: %s", rec.Body.String())
			}
			// Category chips must not expose the locked-only category.
			if rec := serve(t, a, "GET", "/"); strings.Contains(rec.Body.String(), "Audits") {
				t.Fatal("locked category leaks in library")
			}

			// Unknown key stays 404.
			if rec := serve(t, a, "GET", "/view_list?key=lt-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); rec.Code != http.StatusNotFound {
				t.Fatalf("unknown key: %d", rec.Code)
			}

			// Key visit unlocks and grants a cookie.
			rec := serve(t, a, "GET", "/view_list?key="+raw)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Secret audit") {
				t.Fatalf("view_list: %d %s", rec.Code, rec.Body.String())
			}
			unlock := grantCookie(t, rec)
			withGrant := func(method, target string) *httptest.ResponseRecorder {
				return serve(t, a, method, target, unlock)
			}

			if rec := withGrant("GET", "/watch?id="+lockedID); rec.Code != http.StatusOK {
				t.Fatalf("watch with grant: %d", rec.Code)
			}
			if rec := withGrant("GET", mediaPath); rec.Code != http.StatusOK {
				t.Fatalf("media with grant: %d", rec.Code)
			} else if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("locked media cacheable: %q", rec.Header().Get("Cache-Control"))
			}
			// Range requests keep working on unlocked media.
			rng := httptest.NewRequest(http.MethodGet, mediaPath, nil)
			rng.Header.Set("Range", "bytes=0-99")
			rng.AddCookie(unlock)
			rrec := httptest.NewRecorder()
			a.routes().ServeHTTP(rrec, rng)
			if rrec.Code != http.StatusPartialContent {
				t.Fatalf("locked range: %d", rrec.Code)
			}
			if rec := withGrant("GET", "/api/video?id="+lockedID); rec.Code != http.StatusOK {
				t.Fatalf("api single with grant: %d", rec.Code)
			}
			if rec := withGrant("GET", "/"); !strings.Contains(rec.Body.String(), "Secret audit") {
				t.Fatal("library hides unlocked video")
			}
			// Forged cookie grants nothing.
			forged := *unlock
			forged.Value += "x"
			if rec := serve(t, a, "GET", "/watch?id="+lockedID, &forged); rec.Code != http.StatusNotFound {
				t.Fatalf("forged cookie accepted: %d", rec.Code)
			}

			// Direct ?key= works without a cookie.
			if rec := serve(t, a, "GET", "/watch?id="+lockedID+"&key="+raw); rec.Code != http.StatusOK {
				t.Fatalf("watch with direct key: %d", rec.Code)
			}
			if rec := serve(t, a, "GET", mediaPath+"?key="+raw); rec.Code != http.StatusOK {
				t.Fatalf("media with direct key: %d", rec.Code)
			}

			// Public video is unaffected.
			if rec := serve(t, a, "GET", "/watch?id="+publicID); rec.Code != http.StatusOK {
				t.Fatalf("public watch: %d", rec.Code)
			}
			// Management pages render with the visibility picker.
			admin := testSession(t, a, "tester")
			if rec := serve(t, a, "GET", "/upload", admin); rec.Code != http.StatusOK ||
				!strings.Contains(rec.Body.String(), `name="access_list"`) {
				t.Fatalf("upload page: %d", rec.Code)
			}
			if rec := serve(t, a, "GET", "/edit?id="+lockedID, admin); rec.Code != http.StatusOK ||
				!strings.Contains(rec.Body.String(), `value="`+listID+`" selected`) {
				t.Fatalf("edit page: %d", rec.Code)
			}
		})
	}
}

func TestLockedListEditorBypassAndViewerDenied(t *testing.T) {
	a := testApplication(t)
	listID, _ := createLockedList(t, a, "Ασφάλεια")
	lockedID := uploadWithAccessList(t, a, "Guard footage", "Sec", listID)
	locked, _ := a.fs.Get(lockedID)
	mediaPath := "/videos/" + lockedID + "/" + locked.Original

	testUser(t, a, "ed", store.RoleEditor)
	testUser(t, a, "viewer1", store.RoleViewer)
	ed, viewer := testSession(t, a, "ed"), testSession(t, a, "viewer1")

	for _, target := range []string{"/watch?id=" + lockedID, mediaPath, "/api/video?id=" + lockedID} {
		if rec := serve(t, a, "GET", target, ed); rec.Code != http.StatusOK {
			t.Fatalf("editor denied %s: %d", target, rec.Code)
		}
		if rec := serve(t, a, "GET", target, viewer); rec.Code != http.StatusNotFound {
			t.Fatalf("viewer sees %s: %d", target, rec.Code)
		}
	}
	if rec := serve(t, a, "GET", "/api/videos", viewer); !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("viewer list leaks: %s", rec.Body.String())
	}
}

var listKeyPattern = regexp.MustCompile(`lt-[A-Za-z0-9_-]{32}`)

func extractListKey(t *testing.T, body string) string {
	t.Helper()
	key := listKeyPattern.FindString(body)
	if key == "" {
		t.Fatalf("no list key in response: %s", body)
	}
	return key
}

func postListAdmin(t *testing.T, a *application, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(testSession(t, a, "tester"))
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, r)
	return rec
}

func TestLockedListAdminRotationAndDelete(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)

			rec := postListAdmin(t, a, "/api/admin/lists/create", url.Values{"name": {"Έλεγχοι"}})
			if rec.Code != http.StatusOK {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			raw1 := extractListKey(t, rec.Body.String())
			lists, err := a.fs.ListAccessLists()
			if err != nil || len(lists) != 1 {
				t.Fatalf("lists: %+v %v", lists, err)
			}
			listID := lists[0].ID
			lockedID := uploadWithAccessList(t, a, "Audit clip", "Cat", listID)

			if rec := serve(t, a, "GET", "/admin/lists", testSession(t, a, "tester")); rec.Code != http.StatusOK {
				t.Fatalf("dashboard: %d", rec.Code)
			}

			// A grant cookie from before rotation must die with it.
			oldUnlock := grantCookie(t, serve(t, a, "GET", "/view_list?key="+raw1))

			rec = postListAdmin(t, a, "/api/admin/lists/rotate", url.Values{"id": {listID}})
			if rec.Code != http.StatusOK {
				t.Fatalf("rotate: %d %s", rec.Code, rec.Body.String())
			}
			raw2 := extractListKey(t, rec.Body.String())
			if raw1 == raw2 {
				t.Fatal("rotation reused the key")
			}
			if rec := serve(t, a, "GET", "/view_list?key="+raw1); rec.Code != http.StatusNotFound {
				t.Fatalf("old key works: %d", rec.Code)
			}
			if rec := serve(t, a, "GET", "/watch?id="+lockedID, oldUnlock); rec.Code != http.StatusNotFound {
				t.Fatalf("old cookie works after rotation: %d", rec.Code)
			}
			if rec := serve(t, a, "GET", "/view_list?key="+raw2); rec.Code != http.StatusOK {
				t.Fatalf("new key fails: %d", rec.Code)
			}

			// Deletion is blocked while videos are assigned (fail closed).
			rec = postListAdmin(t, a, "/api/admin/lists/delete", url.Values{"id": {listID}})
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("delete redirect: %d", rec.Code)
			}
			if _, err := a.fs.GetAccessList(listID); err != nil {
				t.Fatalf("list deleted while holding videos: %v", err)
			}

			// Moving the video back to public unblocks deletion.
			edit := url.Values{"id": {lockedID}, "title": {"Audit clip"}, "desc": {""}, "category": {"Cat"}}
			r := httptest.NewRequest(http.MethodPost, "/api/edit", strings.NewReader(edit.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(testSession(t, a, "tester"))
			erec := httptest.NewRecorder()
			a.routes().ServeHTTP(erec, r)
			if erec.Code != http.StatusSeeOther && erec.Code != http.StatusOK {
				t.Fatalf("edit to public: %d %s", erec.Code, erec.Body.String())
			}
			rec = postListAdmin(t, a, "/api/admin/lists/delete", url.Values{"id": {listID}})
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("delete after move: %d", rec.Code)
			}
			if rec := serve(t, a, "GET", "/watch?id="+lockedID); rec.Code != http.StatusOK {
				t.Fatalf("moved video not public: %d", rec.Code)
			}
		})
	}
}

func TestLockedListUploadRejectsUnknownList(t *testing.T) {
	a := testApplication(t)
	if id := uploadWithAccessList(t, a, "ok", "c", ""); id == "" {
		t.Fatal("public upload failed")
	}
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.WriteField("title", "bad")
	_ = w.WriteField("access_list", "../escape")
	f, _ := w.CreateFormFile("file", "sample.mp4")
	_, _ = f.Write(sampleVideo(t))
	_ = w.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/upload", &b)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.AddCookie(testSession(t, a, "tester"))
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown list accepted: %d", rec.Code)
	}
}
