package main

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"tiny-tube/store"
)

func renderTmpl(t *testing.T, tmpl *template.Template, data map[string]any) string {
	t.Helper()
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, tmpl.Name(), data); err != nil {
		t.Fatalf("render %s: %v", tmpl.Name(), err)
	}
	return buf.String()
}

func userPtr(role string) *store.User {
	return &store.User{Username: "u", Role: role, Active: true}
}

// TestUploadControlsRequireEditor checks that the upload call-to-action is only
// rendered for editors and administrators, never for anonymous or viewer users.
func TestUploadControlsRequireEditor(t *testing.T) {
	item := store.Meta{ID: "abc", Title: "Δείγμα", Status: "ready", Created: "2026-01-02T15:04:05Z", Views: 3}
	cases := []struct {
		name        string
		user        *store.User
		wantVisible bool
	}{
		{"anonymous", (*store.User)(nil), false},
		{"viewer", userPtr(store.RoleViewer), false},
		{"editor", userPtr(store.RoleEditor), true},
		{"admin", userPtr(store.RoleAdmin), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := renderTmpl(t, listTmpl, map[string]any{
				"Page": "list", "Title": "Βιβλιοθήκη", "Items": []store.Meta{item},
				"Q": "", "Category": "", "Sort": "", "Categories": nil, "Total": 1,
				"CurrentUser": tc.user,
			})
			if got := strings.Contains(list, "library-upload"); got != tc.wantVisible {
				t.Errorf("list hero upload visible=%v, want %v", got, tc.wantVisible)
			}
			viewList := renderTmpl(t, viewListTmpl, map[string]any{
				"Page": "view-list", "Title": "Λίστα", "ListName": "Λίστα", "ListKey": "", "Sort": "recent",
				"Items": []store.Meta{item}, "Total": 1,
				"CurrentUser": tc.user,
			})
			if got := strings.Contains(viewList, "library-upload"); got != tc.wantVisible {
				t.Errorf("view-list hero upload visible=%v, want %v", got, tc.wantVisible)
			}
		})
	}
}

// TestStatusBadgeRequiresEditor checks that conversion status is only shown to
// editors and administrators, not to anonymous or viewer users.
func TestStatusBadgeRequiresEditor(t *testing.T) {
	item := store.Meta{ID: "abc", Title: "Δείγμα", Status: "ready", Created: "2026-01-02T15:04:05Z", Views: 3}
	cases := []struct {
		name        string
		user        *store.User
		wantVisible bool
	}{
		{"anonymous", (*store.User)(nil), false},
		{"viewer", userPtr(store.RoleViewer), false},
		{"editor", userPtr(store.RoleEditor), true},
		{"admin", userPtr(store.RoleAdmin), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := renderTmpl(t, listTmpl, map[string]any{
				"Page": "list", "Title": "Βιβλιοθήκη", "Items": []store.Meta{item},
				"Q": "", "Category": "", "Sort": "", "Categories": nil, "Total": 1,
				"CurrentUser": tc.user,
			})
			if got := strings.Contains(list, "status-badge"); got != tc.wantVisible {
				t.Errorf("list badge visible=%v, want %v", got, tc.wantVisible)
			}
			watch := renderTmpl(t, watchTmpl, map[string]any{
				"Page": "watch", "Title": "Δείγμα", "Video": item,
				"CurrentUser": tc.user,
			})
			if got := strings.Contains(watch, "status-badge"); got != tc.wantVisible {
				t.Errorf("watch badge visible=%v, want %v", got, tc.wantVisible)
			}
			if got := strings.Contains(watch, "Status"); got != tc.wantVisible {
				t.Errorf("watch status row visible=%v, want %v", got, tc.wantVisible)
			}
		})
	}
}
