package main

import (
	"net/http"
	"sort"
	"strconv"
	"tiny-tube/store"
)

func (a *application) queryLibrary(w http.ResponseWriter, r *http.Request, api, locked bool) (store.VideoPage, bool) {
	values := r.URL.Query()
	page, size := 1, 24
	if api {
		size = 50
	}
	for _, entry := range []struct {
		name   string
		target *int
		max    int
	}{{"page", &page, 1000000}, {"page_size", &size, 100}} {
		if raw := values.Get(entry.name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > entry.max {
				writeAPIError(w, r, http.StatusBadRequest, "error.invalidPagination")
				return store.VideoPage{}, false
			}
			*entry.target = n
		}
	}
	grants := a.resolveGrants(w, r)
	q := store.VideoQuery{Text: values.Get("q"), Category: values.Get("category"), Sort: values.Get("sort"), Page: page, PageSize: size, AccessLists: grants, OnlyLocked: locked}
	if u := currentUser(r); u != nil {
		q.All = u.CanEdit()
	}
	if locked {
		if key := values.Get("key"); key != "" {
			list, ok := a.findListByKey(key)
			if !ok {
				http.NotFound(w, r)
				return store.VideoPage{}, false
			}
			q.All = false
			q.AccessLists = map[string]bool{list.ID: true}
		}
	}
	p, err := store.BrowseVideos(a.fs, q)
	if err != nil {
		a.apiServerError(w, r, "list videos", err)
		return p, false
	}
	return p, true
}

func librarySort(r *http.Request) string {
	if sort := r.URL.Query().Get("sort"); sort == "views" || sort == "title" {
		return sort
	}
	return "recent"
}

func paginationData(r *http.Request, p store.VideoPage) map[string]any {
	pages := (p.Matches + p.PageSize - 1) / p.PageSize
	if pages < 1 {
		pages = 1
	}
	link := func(page int) string {
		v := r.URL.Query()
		v.Set("page", strconv.Itoa(page))
		return r.URL.Path + "?" + v.Encode()
	}
	previous, next := "", ""
	if p.Page > 1 {
		previous = link(p.Page - 1)
	}
	if p.Page < pages {
		next = link(p.Page + 1)
	}
	return map[string]any{"PageNumber": p.Page, "PageCount": pages, "PreviousPage": previous, "NextPage": next, "FilteredTotal": p.Matches}
}

func (a *application) renderLibrary(w http.ResponseWriter, r *http.Request, p store.VideoPage) {
	type categoryFilter struct {
		Name     string
		Count    int
		Selected bool
	}
	categories := []categoryFilter{}
	for name, count := range p.Categories {
		categories = append(categories, categoryFilter{name, count, name == r.URL.Query().Get("category")})
	}
	sort.Slice(categories, func(i, j int) bool { return categories[i].Name < categories[j].Name })
	data := paginationData(r, p)
	data["Page"] = "list"
	data["Title"] = "nav.library"
	data["Items"] = p.Items
	data["Q"] = r.URL.Query().Get("q")
	data["Category"] = r.URL.Query().Get("category")
	data["Sort"] = librarySort(r)
	data["Categories"] = categories
	data["Total"] = p.Total
	a.renderPage(w, r, listTmpl, data)
}
