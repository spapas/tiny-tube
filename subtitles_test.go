package main

import (
	"bytes"
	"io"
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

const sampleVTT = "WEBVTT\n\n00:00:00.500 --> 00:00:03.000\nExample cue\n"

func subtitleUploadRequest(t *testing.T, id, language, name, contents string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for name, value := range map[string]string{"id": id, "language": language} {
		if err := form.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	file, err := form.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, contents); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/subtitles/upload", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	return r
}

func subtitleDeleteRequest(id, language string) *http.Request {
	form := url.Values{"id": {id}, "language": {language}}
	r := httptest.NewRequest("POST", "/api/subtitles/delete", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestSubtitleLifecycleAndVisibility(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			id := uploadWithAccessList(t, a, "Training", "", "")
			editor := testSession(t, a, "tester")
			h := a.routes()
			post := func(r *http.Request, status int) *httptest.ResponseRecorder {
				t.Helper()
				r.AddCookie(editor)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != status {
					t.Fatalf("%s: got %d, want %d: %s", r.URL.Path, w.Code, status, w.Body.String())
				}
				return w
			}
			if w := post(subtitleUploadRequest(t, id, "EN-us", "arbitrary.VTT", sampleVTT), 303); w.Header().Get("Location") != "/edit?id="+id+"#subtitles" {
				t.Fatal("subtitle upload did not redirect to its edit section")
			}
			greek := "\ufeffWEBVTT\r\n\r\n00:00:00.500 --> 00:00:03.000\r\nΔοκιμή\r\n"
			post(subtitleUploadRequest(t, id, "el", "greek.vtt", greek), 303)
			updated := strings.Replace(sampleVTT, "Example cue", "Replacement cue", 1)
			post(subtitleUploadRequest(t, id, "en-US", "new.vtt", updated), 303)
			post(subtitleUploadRequest(t, id, "en-us", "bad.vtt", "invalid file"), 415)

			watchPath := "/watch?id=" + id
			mediaPath := "/videos/" + id + "/subtitles/track-en-us.vtt"
			watch := serve(t, a, "GET", watchPath)
			if watch.Code != 200 || strings.Count(watch.Body.String(), `<track kind="subtitles"`) != 2 ||
				!strings.Contains(watch.Body.String(), `src="`+mediaPath+`" srclang="en-US"`) {
				t.Fatal("watch page did not expose the canonical subtitle tracks")
			}
			if edit := serve(t, a, "GET", "/edit?id="+id, editor); edit.Code != 200 || !strings.Contains(edit.Body.String(), `name="language" value="en-US"`) {
				t.Fatal("edit page did not show the saved subtitle")
			}
			vtt := serve(t, a, "GET", mediaPath)
			if vtt.Code != 200 || vtt.Body.String() != updated || !strings.HasPrefix(vtt.Header().Get("Content-Type"), "text/vtt") || vtt.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("subtitle media returned %d %v %q", vtt.Code, vtt.Header(), vtt.Body.String())
			}
			r := httptest.NewRequest("GET", mediaPath, nil)
			r.Header.Set("Range", "bytes=0-5")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 206 || w.Body.String() != "WEBVTT" {
				t.Fatal("subtitle media did not preserve Range behavior")
			}

			list, key := createLockedList(t, a, "Private")
			m, err := a.fs.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			m.AccessList = list
			if err := a.putMeta(m); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{watchPath, mediaPath} {
				if w := serve(t, a, "GET", path); w.Code != 404 {
					t.Fatalf("locked subtitle surface was exposed: %s (%d)", path, w.Code)
				}
			}
			granted := serve(t, a, "GET", watchPath+"&key="+key)
			if granted.Code != 200 || !strings.Contains(granted.Body.String(), mediaPath) {
				t.Fatal("key holder did not see the subtitle track")
			}
			for _, w := range []*httptest.ResponseRecorder{
				serve(t, a, "GET", mediaPath, grantCookie(t, granted)),
				serve(t, a, "GET", mediaPath+"?key="+key),
				serve(t, a, "GET", mediaPath, editor),
			} {
				if w.Code != 200 || w.Body.String() != updated {
					t.Fatal("authorized subtitle retrieval failed", w.Code)
				}
			}
			post(subtitleDeleteRequest(id, "en-US"), 303)
			post(subtitleDeleteRequest(id, "en-US"), 404)
			if w := serve(t, a, "GET", mediaPath, editor); w.Code != 404 {
				t.Fatal("deleted subtitle still served", w.Code)
			}
			if w := serve(t, a, "GET", strings.Replace(mediaPath, "track-en-us.vtt", "track-el.vtt", 1), editor); w.Code != 200 || w.Body.String() != greek {
				t.Fatal("deleting one language affected another", w.Code)
			}
		})
	}
}

func TestSubtitleMutationsRequireEditorAndValidateInput(t *testing.T) {
	a := testApplication(t)
	id := uploadWithAccessList(t, a, "Training", "", "")
	testUser(t, a, "viewer", store.RoleViewer)
	testUser(t, a, "editor", store.RoleEditor)
	viewer, editor := testSession(t, a, "viewer"), testSession(t, a, "editor")
	h := a.routes()
	check := func(r *http.Request, session *http.Cookie, origin string, status int) {
		t.Helper()
		if session != nil {
			r.AddCookie(session)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		r.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s: got %d %q, want %d and JSON", r.URL.Path, w.Code, w.Body.String(), status)
		}
	}
	for _, request := range []func() *http.Request{
		func() *http.Request { return subtitleUploadRequest(t, id, "en", "a.vtt", sampleVTT) },
		func() *http.Request { return subtitleDeleteRequest(id, "en") },
	} {
		check(request(), nil, "", 401)
		check(request(), viewer, "", 403)
		check(request(), editor, "https://other.example", 403)
	}
	check(subtitleUploadRequest(t, id, "../../outside", "a.vtt", sampleVTT), editor, "", 400)
	check(subtitleUploadRequest(t, "missing", "en", "a.vtt", sampleVTT), editor, "", 404)
	check(subtitleUploadRequest(t, id, "en", "a.srt", sampleVTT), editor, "", 415)
	check(subtitleUploadRequest(t, id, "en", "a.vtt", "WEBVTT\n\n\xff"), editor, "", 415)
	check(subtitleUploadRequest(t, id, "en", "a.vtt", sampleVTT+strings.Repeat("x", maxSubtitleBytes)), editor, "", 413)
	check(subtitleUploadRequest(t, id, "en", "a.vtt", sampleVTT+strings.Repeat("x", maxSubtitleRequest)), editor, "", 413)
	if _, err := os.Stat(filepath.Join(a.fs.Dir(id), subtitleDir)); !os.IsNotExist(err) {
		t.Fatal("a rejected upload left subtitle files", err)
	}
	check(subtitleUploadRequest(t, id, "en", "a.vtt", sampleVTT), editor, "", 200)
	check(subtitleDeleteRequest(id, "en"), editor, "", 200)
}

func TestSubtitleMediaBlocksStagingAndDatabaseAliases(t *testing.T) {
	a := testApplicationWithStore(t, "sqlite")
	id := uploadWithAccessList(t, a, "Training", "", "")
	dir := filepath.Join(a.fs.Dir(id), subtitleDir)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a.cfg.DBPath, filepath.Join(dir, "track-en.vtt")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, ".upload-en.vtt"), filepath.Join(a.fs.Dir(id), "track-en.vtt")} {
		if err := os.WriteFile(path, []byte(sampleVTT), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"subtitles/track-en.vtt", "subtitles/.upload-en.vtt", "track-en.vtt"} {
		if w := serve(t, a, "GET", "/videos/"+id+"/"+path); w.Code != 404 {
			t.Fatalf("unexpected public subtitle path %s (%d)", path, w.Code)
		}
	}
}
