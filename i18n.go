package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const languageCookieName = "tiny_tube_language"

// Catalogs use the original UI message as the key, including printf placeholders.
// They are embedded in the binary; templates and browser scripts share them.
var catalogs = loadCatalogs()

var browserTranslations = buildBrowserTranslations()

func buildBrowserTranslations() map[string][]byte {
	script, err := webFiles.ReadFile("web/static/i18n.js")
	if err != nil {
		panic(err)
	}
	result := make(map[string][]byte)
	for lang, messages := range catalogs {
		data, err := json.Marshal(messages)
		if err != nil {
			panic(err)
		}
		result[lang+".js"] = []byte("window.tinyTubeMessages = " + string(data) + ";\n" + string(script))
	}
	return result
}

func serveTranslations(w http.ResponseWriter, r *http.Request) {
	script, ok := browserTranslations[r.PathValue("language")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(script)
}

func loadCatalogs() map[string]map[string]string {
	result := make(map[string]map[string]string)
	for _, lang := range []string{"el", "en"} {
		data, err := webFiles.ReadFile("web/locales/" + lang + ".json")
		if err != nil {
			panic(err)
		}
		var messages map[string]string
		if err := json.Unmarshal(data, &messages); err != nil {
			panic(err)
		}
		result[lang] = messages
	}
	return result
}

func translate(lang, message string, args ...any) string {
	if translated, ok := catalogs[lang][message]; ok {
		message = translated
	}
	if len(args) > 0 {
		return fmt.Sprintf(message, args...)
	}
	return message
}

func requestLanguage(r *http.Request) string {
	if cookie, err := r.Cookie(languageCookieName); err == nil {
		if cookie.Value == "el" || cookie.Value == "en" {
			return cookie.Value
		}
	}
	// Match supported language families, honoring weights and header order.
	// Unsupported languages (including *) leave the Greek default in place.
	best, quality := "el", 0.0
	for _, entry := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		parts := strings.Split(entry, ";")
		lang := strings.ToLower(strings.TrimSpace(parts[0]))
		lang, _, _ = strings.Cut(lang, "-")
		if lang != "el" && lang != "en" {
			continue
		}
		q := 1.0
		if len(parts) > 1 {
			parameter := strings.TrimSpace(parts[1])
			if !strings.HasPrefix(parameter, "q=") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimPrefix(parameter, "q="), 64)
			if err != nil || parsed < 0 || parsed > 1 {
				continue
			}
			q = parsed
		}
		if q > quality {
			best, quality = lang, q
		}
	}
	return best
}

func localizedHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/assets/") && !strings.HasPrefix(r.URL.Path, "/videos/") && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			w.Header().Set("Content-Language", requestLanguage(r))
			w.Header().Add("Vary", "Accept-Language")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *application) handleLanguage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "invalid form")
		return
	}
	lang := r.PostForm.Get("language")
	if lang != "el" && lang != "en" {
		writeAPIError(w, r, http.StatusBadRequest, "unsupported language")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: languageCookieName, Value: lang, Path: "/",
		MaxAge: 365 * 24 * 60 * 60, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: a.cfg.CookieSecure,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, localRedirectTarget(r.PostForm.Get("next")), http.StatusSeeOther)
}

// Each language gets its own immutable templates/functions. Never mutate a
// template's function map while serving concurrent requests in other languages.
func localizedTemplates(base *template.Template) map[string]*template.Template {
	result := make(map[string]*template.Template)
	for _, lang := range []string{"el", "en"} {
		t := func(message string, args ...any) string { return translate(lang, message, args...) }
		cloned := template.Must(base.Clone()).Funcs(template.FuncMap{
			"t": t,
			"formatDate": func(created string) string {
				if lang == "en" {
					date, err := time.Parse(time.RFC3339Nano, created)
					if err != nil {
						return t("Χωρίς ημερομηνία")
					}
					return date.Format("2 Jan 2006")
				}
				return formatDate(created)
			},
			"formatBytes": func(n int64) string {
				formatted := formatBytes(n)
				if lang == "en" {
					return strings.ReplaceAll(formatted, ",", ".")
				}
				return formatted
			},
			"statusLabel": func(status string) string { return t(statusLabel(status)) },
			"videoCount": func(n int) string {
				if n == 1 {
					return t("1 βίντεο")
				}
				return t("%d βίντεο", n)
			},
			"viewCount": func(n int64) string {
				if n == 1 {
					return t("1 προβολή")
				}
				return t("%d προβολές", n)
			},
		})
		result[lang] = cloned
	}
	return result
}
