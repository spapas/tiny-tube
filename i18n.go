package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"
)

const languageCookieName = "tiny_tube_language"
const defaultLanguage = "en"

// Catalogs are standard go-i18n JSON files. English owns the message IDs and
// source text; adding a locale file automatically registers it everywhere.
type catalogMessage struct {
	Description string `json:"description,omitempty"`
	Hash        string `json:"hash,omitempty"`
	Zero        string `json:"zero,omitempty"`
	One         string `json:"one,omitempty"`
	Two         string `json:"two,omitempty"`
	Few         string `json:"few,omitempty"`
	Many        string `json:"many,omitempty"`
	Other       string `json:"other"`
}

type languageOption struct{ Code, Name string }

type translationSet struct {
	catalogs   map[string]map[string]catalogMessage
	languages  []languageOption
	localizers map[string]*i18n.Localizer
	printers   map[string]*message.Printer
}

var messageIDPattern = regexp.MustCompile(`^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9]+)+$`)
var messageVariablePattern = regexp.MustCompile(`\{\{\s*\.([A-Za-z][A-Za-z0-9]*)\s*\}\}`)
var appTranslations = mustLoadTranslations(webFiles)
var browserTranslations = buildBrowserTranslations()

func mustLoadTranslations(files fs.FS) *translationSet {
	result, err := loadTranslations(files)
	if err != nil {
		panic(err)
	}
	return result
}

func loadTranslations(files fs.FS) (*translationSet, error) {
	filenames, err := fs.Glob(files, "web/locales/*.json")
	if err != nil {
		return nil, err
	}
	set := &translationSet{
		catalogs:   make(map[string]map[string]catalogMessage),
		localizers: make(map[string]*i18n.Localizer), printers: make(map[string]*message.Printer),
	}
	for _, filename := range filenames {
		code := strings.TrimSuffix(path.Base(filename), ".json")
		tag, err := language.Parse(code)
		if err != nil || tag.String() != code {
			return nil, fmt.Errorf("invalid locale filename: %s", filename)
		}
		data, err := fs.ReadFile(files, filename)
		if err != nil {
			return nil, err
		}
		var messages map[string]catalogMessage
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&messages); err != nil {
			return nil, fmt.Errorf("%s: %w", filename, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("%s: expected one JSON object", filename)
		}
		if messages["language.name"].Other == "" {
			return nil, fmt.Errorf("%s: language.name is required", filename)
		}
		set.catalogs[code] = messages
	}
	if len(set.catalogs[defaultLanguage]) == 0 {
		return nil, fmt.Errorf("English source catalog is required")
	}
	codes := []string{defaultLanguage}
	for code := range set.catalogs {
		if code != defaultLanguage {
			codes = append(codes, code)
		}
	}
	slices.Sort(codes[1:])
	bundle := i18n.NewBundle(language.English)
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)
	for _, code := range codes {
		for id, msg := range set.catalogs[code] {
			if err := validateCatalogMessage(id, msg, set.catalogs[defaultLanguage]); err != nil {
				return nil, fmt.Errorf("%s: %w", code, err)
			}
		}
		data, err := json.Marshal(set.catalogs[code])
		if err != nil {
			return nil, err
		}
		if _, err := bundle.ParseMessageFileBytes(data, code+".json"); err != nil {
			return nil, err
		}
		tag := language.Make(code)
		set.languages = append(set.languages, languageOption{code, set.catalogs[code]["language.name"].Other})
		set.printers[code] = message.NewPrinter(tag)
	}
	for _, code := range codes {
		set.localizers[code] = i18n.NewLocalizer(bundle, code)
	}
	return set, nil
}

// Limit message templates to named substitutions, shared by text/template and
// the browser. Reject typos/unsupported template operations before serving.
func validateCatalogMessage(id string, msg catalogMessage, english map[string]catalogMessage) error {
	source, exists := english[id]
	if !messageIDPattern.MatchString(id) || !exists {
		return fmt.Errorf("unknown message ID %q", id)
	}
	if strings.TrimSpace(msg.Other) == "" {
		return fmt.Errorf("%s: other is required", id)
	}
	want := messageVariables(source.Other)
	for _, text := range []string{msg.Zero, msg.One, msg.Two, msg.Few, msg.Many, msg.Other} {
		if text == "" {
			continue
		}
		rest := messageVariablePattern.ReplaceAllString(text, "")
		variables := messageVariables(text)
		compatible := true
		for _, name := range variables {
			compatible = compatible && slices.Contains(want, name)
		}
		for _, name := range want {
			// A plural form may express its count in words ("No videos").
			compatible = compatible && (name == "Count" || slices.Contains(variables, name))
		}
		if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") || !compatible {
			return fmt.Errorf("%s: use the source named variables %v", id, want)
		}
	}
	return nil
}

func messageVariables(text string) []string {
	var result []string
	for _, match := range messageVariablePattern.FindAllStringSubmatch(text, -1) {
		result = append(result, match[1])
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func (set *translationSet) text(code, id string, args ...any) string {
	localizer, ok := set.localizers[code]
	if !ok {
		code, localizer = defaultLanguage, set.localizers[defaultLanguage]
	}
	data := make(map[string]any)
	for i := 0; i+1 < len(args); i += 2 {
		name, ok := args[i].(string)
		if !ok {
			return id
		}
		data[name] = args[i+1]
	}
	config := &i18n.LocalizeConfig{MessageID: id, TemplateData: data}
	if count, ok := data["Count"]; ok {
		config.PluralCount = count
		data["Count"] = set.printers[code].Sprintf("%v", number.Decimal(count))
	}
	translated, _ := localizer.Localize(config)
	if translated == "" {
		return id
	}
	return translated
}

func translate(code, id string, args ...any) string {
	return appTranslations.text(code, id, args...)
}

func (set *translationSet) requestLanguage(r *http.Request) string {
	if cookie, err := r.Cookie(languageCookieName); err == nil {
		if _, ok := set.catalogs[cookie.Value]; ok {
			return cookie.Value
		}
	}
	if _, ok := set.catalogs["el"]; ok {
		return "el"
	}
	return defaultLanguage
}

func requestLanguage(r *http.Request) string { return appTranslations.requestLanguage(r) }

func buildBrowserTranslations() map[string][]byte {
	script, err := webFiles.ReadFile("web/static/i18n.js")
	if err != nil {
		panic(err)
	}
	result := make(map[string][]byte)
	for _, option := range appTranslations.languages {
		// Merge missing messages from English before sending the browser catalog.
		type browserMessage struct {
			catalogMessage
			Language string `json:"language"`
		}
		messages := make(map[string]browserMessage)
		for id, msg := range appTranslations.catalogs[defaultLanguage] {
			messages[id] = browserMessage{msg, defaultLanguage}
		}
		for id, msg := range appTranslations.catalogs[option.Code] {
			messages[id] = browserMessage{msg, option.Code}
		}
		data, err := json.Marshal(messages)
		if err != nil {
			panic(err)
		}
		result[option.Code+".js"] = []byte("window.tinyTubeMessages = " + string(data) + ";\n" + string(script))
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

func localizedHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/assets/") && !strings.HasPrefix(r.URL.Path, "/videos/") && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			w.Header().Set("Content-Language", requestLanguage(r))
			w.Header().Add("Vary", "Cookie")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *application) handleLanguage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "error.invalidForm")
		return
	}
	code := r.PostForm.Get("language")
	if _, ok := appTranslations.catalogs[code]; !ok {
		writeAPIError(w, r, http.StatusBadRequest, "error.unsupportedLanguage")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: languageCookieName, Value: code, Path: "/",
		MaxAge: 365 * 24 * 60 * 60, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: a.cfg.CookieSecure,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, localRedirectTarget(r.PostForm.Get("next")), http.StatusSeeOther)
}

func (set *translationSet) templateFuncs(code string) template.FuncMap {
	t := func(id string, args ...any) string { return set.text(code, id, args...) }
	printer := set.printers[code]
	return template.FuncMap{
		"t": t,
		"formatDate": func(created string) string {
			date, err := time.Parse(time.RFC3339Nano, created)
			if err != nil {
				return t("date.unknown")
			}
			day := printer.Sprintf("%v", number.Decimal(date.Day(), number.NoSeparator()))
			year := printer.Sprintf("%v", number.Decimal(date.Year(), number.NoSeparator()))
			return t("date.format", "Day", day, "Month", t(fmt.Sprintf("date.month.%d", date.Month())), "Year", year)
		},
		"formatBytes": func(n int64) string {
			for _, unit := range []struct {
				bytes int64
				name  string
			}{{1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}} {
				if n >= unit.bytes {
					value := printer.Sprintf("%v", number.Decimal(float64(n)/float64(unit.bytes), number.MaxFractionDigits(1)))
					return t("size.value", "Value", value, "Unit", unit.name)
				}
			}
			return t("size.bytes", "Count", n)
		},
		"formatSpeed": func(speed float64) string {
			return printer.Sprintf("%v", number.Decimal(speed, number.MaxFractionDigits(2))) + "×"
		},
		"statusLabel": func(status string) string {
			switch status {
			case "ready", "queued", "processing", "failed":
			default:
				status = "unknown"
			}
			return t("status." + status)
		},
		"videoCount": func(n int) string { return t("count.videos", "Count", n) },
		"viewCount":  func(n int64) string { return t("count.views", "Count", n) },
	}
}

// Each locale has immutable functions/templates; requests never mutate them.
func localizedTemplates(base *template.Template) map[string]*template.Template {
	result := make(map[string]*template.Template)
	for _, option := range appTranslations.languages {
		result[option.Code] = template.Must(base.Clone()).Funcs(appTranslations.templateFuncs(option.Code))
	}
	return result
}
