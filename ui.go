package main

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed web/templates/*.html web/static/*
var webFiles embed.FS

var pageTemplates = template.Must(template.New("pages").Funcs(template.FuncMap{
	"hlsScriptURL": func() string { return hlsScriptURL },
	"formatDate":   formatDate,
	"formatBytes":  formatBytes,
	"statusLabel":  statusLabel,
	"statusClass":  statusClass,
	"videoCount":   func(n int) string { return fmt.Sprintf("%d βίντεο", n) },
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
}).ParseFS(webFiles, "web/templates/*.html"))

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

func formatDate(created string) string {
	t, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return "Χωρίς ημερομηνία"
	}
	months := [...]string{"Ιαν", "Φεβ", "Μαρ", "Απρ", "Μάι", "Ιουν", "Ιουλ", "Αυγ", "Σεπ", "Οκτ", "Νοε", "Δεκ"}
	return fmt.Sprintf("%d %s %d", t.Day(), months[t.Month()-1], t.Year())
}

func formatBytes(n int64) string {
	for _, unit := range []struct {
		bytes int64
		name  string
	}{{1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}} {
		if n >= unit.bytes {
			value := strconv.FormatFloat(float64(n)/float64(unit.bytes), 'f', 1, 64)
			value = strings.TrimSuffix(value, ".0")
			return strings.ReplaceAll(value, ".", ",") + " " + unit.name
		}
	}
	return fmt.Sprintf("%d bytes", n)
}

func statusLabel(status string) string {
	switch status {
	case "ready":
		return "Έτοιμο"
	case "queued":
		return "Στην ουρά"
	case "processing":
		return "Ετοιμάζεται"
	case "failed":
		return "Χρειάζεται έλεγχο"
	default:
		return "Άγνωστη"
	}
}

func statusClass(status string) string {
	switch status {
	case "ready", "queued", "processing", "failed":
		return status
	default:
		return "queued"
	}
}
