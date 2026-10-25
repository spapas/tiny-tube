package main

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"tiny-tube/store"
)

func TestLanguageNegotiation(t *testing.T) {
	for _, tc := range []struct{ header, cookie, want string }{
		{"", "", "el"}, {"fr, de", "", "el"},
		{"en-GB,en;q=0.9,el;q=0.8", "", "en"},
		{"en;q=0.5,el-GR;q=0.9", "", "el"},
		{"el;q=0.5,en;q=0.5", "", "el"},
		{"en;q=0", "", "el"}, {"en;q=2", "", "el"},
		{"en;q=invalid", "", "el"}, {"EN-us", "", "en"},
		{"en", "el", "el"}, {"el", "en", "en"},
		{"en", "invalid", "en"},
	} {
		t.Run(tc.header+"/"+tc.cookie, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Accept-Language", tc.header)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: languageCookieName, Value: tc.cookie})
			}
			if got := requestLanguage(r); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLanguageSwitchPreservesLocationAndRejectsInvalidRequests(t *testing.T) {
	a := testApplication(t)
	a.cfg.CookieSecure = true
	h := a.routes()
	for _, next := range []string{"/?q=Go&category=Εκπαίδευση&sort=views&page=2", "/watch?id=video&key=lt-key", "//outside.test", "https://outside.test", "/\\outside.test", "/\nLocation:bad"} {
		t.Run(next, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/language", strings.NewReader(url.Values{"language": {"en"}, "next": {next}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			location, err := url.PathUnescape(w.Header().Get("Location"))
			if err != nil || w.Code != http.StatusSeeOther || location != localRedirectTarget(next) {
				t.Fatalf("redirect: %d %q", w.Code, w.Header().Get("Location"))
			}
			cookies := w.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != languageCookieName || cookies[0].Value != "en" || cookies[0].Path != "/" || cookies[0].MaxAge != 31536000 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
				t.Fatalf("language cookie: %+v", cookies)
			}
			page := httptest.NewRequest("GET", "/login", nil)
			page.AddCookie(cookies[0])
			result := httptest.NewRecorder()
			h.ServeHTTP(result, page)
			if !strings.Contains(result.Body.String(), `lang="en"`) {
				t.Fatal("language preference was not retained")
			}
		})
	}
	for _, tc := range []struct {
		body, origin string
		code         int
	}{
		{"language=fr", "", 400}, {"language=en", "https://other.test", 403},
		{"language=en&next=" + strings.Repeat("x", 5000), "", 400},
	} {
		r := httptest.NewRequest("POST", "/language", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code || len(w.Result().Cookies()) != 0 {
			t.Fatalf("invalid request accepted: %d", w.Code)
		}
	}
}

func TestLocalizedPagesAndUserContent(t *testing.T) {
	a := testApplication(t)
	a.cfg.MaxUploadBytes = 1536
	m := store.Meta{ID: "localized", Title: "Βιβλιοθήκη", Category: "Περιγραφή", Desc: `<script>alert("test")</script>`, Created: "2026-10-25T12:00:00Z", Status: "queued", Original: "original.mp4", Views: 2}
	if err := a.fs.Put(m); err != nil {
		t.Fatal(err)
	}
	session := testSession(t, a, "tester")
	h := a.routes()
	for _, lang := range []string{"el", "en"} {
		for _, path := range []string{"/", "/?q=unmatched", "/watch?id=localized", "/login?next=%2Fupload", "/upload", "/edit?id=localized", "/view_list", "/admin", "/admin/users?notice=user+created", "/admin/users/new", "/admin/keys", "/admin/lists"} {
			t.Run(lang+path, func(t *testing.T) {
				r := httptest.NewRequest("GET", path, nil)
				r.AddCookie(&http.Cookie{Name: languageCookieName, Value: lang})
				if !strings.HasPrefix(path, "/login") {
					r.AddCookie(session)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				body := w.Body.String()
				if w.Code != 200 || !strings.Contains(body, `lang="`+lang+`"`) || w.Header().Get("Content-Language") != lang || !strings.Contains(body, translate(lang, "Μετάβαση στο περιεχόμενο")) {
					t.Fatalf("localized page: %d", w.Code)
				}
				if !strings.Contains(body, `/assets/i18n/`+lang+`.js?v=1`) || !strings.Contains(body, `action="/language"`) || !strings.Contains(body, `aria-pressed="true"`) {
					t.Fatal("missing language resources or selector")
				}
				if strings.Contains(body, m.Desc) {
					t.Fatal("user content was not escaped")
				}
				if path == "/watch?id=localized" {
					for _, text := range []string{"<title>Βιβλιοθήκη · Tiny Tube</title>", `id="video-title">Βιβλιοθήκη</h1>`, html.EscapeString(m.Desc), translate(lang, "Στην ουρά")} {
						if !strings.Contains(body, text) {
							t.Fatalf("missing text %q", text)
						}
					}
					date := "25 Οκτ 2026"
					if lang == "en" {
						date = "25 Oct 2026"
					}
					if !strings.Contains(body, date) {
						t.Fatal("date was not localized")
					}
				}
				if path == "/admin/users?notice=user+created" && !strings.Contains(body, translate(lang, "user created")) {
					t.Fatal("notice was not localized")
				}
				if path == "/upload" {
					size := "1,5 KiB"
					if lang == "en" {
						size = "1.5 KiB"
					}
					if !strings.Contains(body, size) {
						t.Fatal("size was not localized")
					}
				}
			})
		}
	}
}

func TestConcurrentLanguageRendering(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	var workers sync.WaitGroup
	for i := range 40 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			lang := []string{"el", "en"}[i%2]
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Accept-Language", lang)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "<h1 id=\"library-title\">"+translate(lang, "Βιβλιοθήκη βίντεο")+"</h1>") {
				t.Errorf("language crossed between requests: %s", lang)
			}
		}()
	}
	workers.Wait()
}

func TestLocalizedErrorsAndStableJSON(t *testing.T) {
	for _, lang := range []string{"el", "en"} {
		for _, json := range []bool{false, true} {
			r := httptest.NewRequest("POST", "/api/edit", nil)
			r.Header.Set("Accept-Language", lang)
			if json {
				r.Header.Set("Accept", "application/json")
			}
			w := httptest.NewRecorder()
			writeAPIError(w, r, 400, "invalid form")
			want := translate(lang, "invalid form")
			if json {
				want = `{"error":"invalid form"}`
			}
			if w.Code != 400 || !strings.Contains(w.Body.String(), want) {
				t.Fatalf("error %s json=%v: %s", lang, json, w.Body.String())
			}
		}
	}
}

func TestTranslationCatalogCoverage(t *testing.T) {
	for key, value := range catalogs["el"] {
		en, ok := catalogs["en"][key]
		if !ok || value == "" || en == "" {
			t.Errorf("missing translation: %s", key)
		}
		if fmt.Sprint(regexp.MustCompile(`%[sd]`).FindAllString(value, -1)) != fmt.Sprint(regexp.MustCompile(`%[sd]`).FindAllString(en, -1)) {
			t.Errorf("placeholder mismatch: %s", key)
		}
	}
	if len(catalogs["el"]) != len(catalogs["en"]) {
		t.Fatal("catalog keys differ")
	}
	files, err := webFiles.ReadDir("web/templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := webFiles.ReadFile("web/templates/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range regexp.MustCompile(`\{\{t ("(?:[^"\\]|\\.)*")`).FindAllStringSubmatch(string(data), -1) {
			key, err := strconv.Unquote(match[1])
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := catalogs["en"][key]; !ok {
				t.Errorf("%s: missing %q", file.Name(), key)
			}
		}
	}
	if translate("en", "%d βίντεο", 2) != "2 videos" || translate("en", "1 προβολή") != "1 view" || translate("en", "unknown message") != "unknown message" {
		t.Fatal("count or fallback translation failed")
	}
}

func TestTranslationAssets(t *testing.T) {
	h := testApplication(t).routes()
	for _, lang := range []string{"el", "en", "fr"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/assets/i18n/"+lang+".js", nil))
		if lang == "fr" {
			if w.Code != 404 {
				t.Fatal("unsupported translation asset was served")
			}
			continue
		}
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "javascript") || !strings.Contains(w.Body.String(), "tinyTubeTranslate") {
			t.Fatalf("translation asset: %d", w.Code)
		}
	}
}
