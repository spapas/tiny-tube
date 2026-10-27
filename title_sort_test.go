package main

import (
	"fmt"
	"strings"
	"testing"

	"tiny-tube/store"
)

func TestTitleSortLibraryAndLockedList(t *testing.T) {
	for _, backend := range []string{"fs", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			a := testApplicationWithStore(t, backend)
			list, key := createLockedList(t, a, "Lessons")
			for i, title := range []string{"Μάθημα 10", "Μάθημα 2", "Μάθημα 1"} {
				m := store.Meta{ID: fmt.Sprintf("lesson-%d", i), Title: title, Category: "Lessons", Created: "2026-10-01T00:00:00Z", AccessList: list}
				if err := a.putMeta(m); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{"/?sort=title&category=Lessons&key=" + key, "/view_list?sort=title&key=" + key, "/api/videos?sort=title&key=" + key} {
				rec := serve(t, a, "GET", path)
				body := rec.Body.String()
				p1, p2, p10 := strings.Index(body, "Μάθημα 1\""), strings.Index(body, "Μάθημα 2"), strings.Index(body, "Μάθημα 10")
				if !strings.Contains(path, "/api/") {
					p1 = strings.Index(body, "Μάθημα 1</h3>")
				}
				if rec.Code != 200 || p1 < 0 || p2 < p1 || p10 < p2 {
					t.Fatalf("%s: %d %s", path, rec.Code, body)
				}
				if strings.Contains(path, "/view_list") && !strings.Contains(body, "key="+key+"&amp;sort=title") {
					t.Fatal("locked list sort link lost key")
				}
				if strings.HasPrefix(path, "/?") && !strings.Contains(body, `name="sort" value="title"`) {
					t.Fatal("search lost sort")
				}
			}
		})
	}
}
