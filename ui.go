package main

import (
	"embed"
	"html/template"
	"io/fs"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

//go:embed web/templates/*.html web/static/* web/locales/*.json
var webFiles embed.FS

var pageTemplates = template.Must(template.New("pages").Funcs(template.FuncMap{
	"hlsScriptURL":     func() string { return hlsScriptURL },
	"statusClass":      statusClass,
	"descriptionLines": descriptionLines,
	"filterURL": func(q, category string) string {
		values := url.Values{}
		if q != "" {
			values.Set("q", q)
		}
		if category != "" {
			values.Set("category", category)
		}
		if len(values) == 0 {
			return "/"
		}
		return "/?" + values.Encode()
	},
	"listSortURL": func(key, sort string) string {
		values := url.Values{}
		if key != "" {
			values.Set("key", key)
		}
		if sort != "" {
			values.Set("sort", sort)
		}
		if len(values) == 0 {
			return "/view_list"
		}
		return "/view_list?" + values.Encode()
	},
	"sortURL": func(q, category, sort string) string {
		values := url.Values{}
		if q != "" {
			values.Set("q", q)
		}
		if category != "" {
			values.Set("category", category)
		}
		if sort == "views" || sort == "title" {
			values.Set("sort", sort)
		}
		if len(values) == 0 {
			return "/"
		}
		return "/?" + values.Encode()
	},
}).Funcs(appTranslations.templateFuncs(defaultLanguage)).ParseFS(webFiles, "web/templates/*.html"))

var languageTemplates = localizedTemplates(pageTemplates)

var (
	listTmpl         = pageTemplates.Lookup("list")
	watchTmpl        = pageTemplates.Lookup("watch")
	viewListTmpl     = pageTemplates.Lookup("view-list")
	uploadTmpl       = pageTemplates.Lookup("upload")
	editTmpl         = pageTemplates.Lookup("edit")
	loginTmpl        = pageTemplates.Lookup("login")
	adminTmpl        = pageTemplates.Lookup("admin")
	adminUsersTmpl   = pageTemplates.Lookup("admin-users")
	adminUserNewTmpl = pageTemplates.Lookup("admin-user-new")
	adminKeysTmpl    = pageTemplates.Lookup("admin-keys")
	adminListsTmpl   = pageTemplates.Lookup("admin-lists")
)

func staticFiles() fs.FS {
	assets, err := fs.Sub(webFiles, "web/static")
	if err != nil {
		panic(err)
	}
	return assets
}

func statusClass(status string) string {
	switch status {
	case "ready", "queued", "processing", "failed":
		return status
	default:
		return "queued"
	}
}

var chapterLine = regexp.MustCompile(`^[\t ]*(?:([0-9]{1,6}):)?([0-5]?[0-9]):([0-5][0-9])[\t ]+\S`)

type descriptionLine struct {
	Text     string
	Fragment string
}

func descriptionLines(description string) []descriptionLine {
	lines := strings.Split(description, "\n")
	result := make([]descriptionLine, 0, len(lines))
	for _, line := range lines {
		part := descriptionLine{Text: line}
		if match := chapterLine.FindStringSubmatch(line); match != nil {
			hours, _ := strconv.Atoi(match[1])
			minutes, _ := strconv.Atoi(match[2])
			seconds, _ := strconv.Atoi(match[3])
			part.Fragment = "#t=" + strconv.FormatInt(int64(hours)*3600+int64(minutes)*60+int64(seconds), 10)
		}
		result = append(result, part)
	}
	return result
}
