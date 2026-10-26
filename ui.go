package main

import (
	"embed"
	"html/template"
	"io/fs"
	"net/url"
)

//go:embed web/templates/*.html web/static/* web/locales/*.json
var webFiles embed.FS

var pageTemplates = template.Must(template.New("pages").Funcs(template.FuncMap{
	"hlsScriptURL": func() string { return hlsScriptURL },
	"statusClass":  statusClass,
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
	"sortURL": func(q, category, sort string) string {
		values := url.Values{}
		if q != "" {
			values.Set("q", q)
		}
		if category != "" {
			values.Set("category", category)
		}
		if sort == "views" {
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
