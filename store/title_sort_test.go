package store

import (
	"fmt"
	"reflect"
	"testing"
)

func TestTitleSortAcrossPagesAndStores(t *testing.T) {
	for name, s := range map[string]Store{"fs": NewFS(t.TempDir()), "sqlite": newTestSQLite(t)} {
		t.Run(name, func(t *testing.T) {
			titles := []string{"Μάθημα 10", "Μάθημα 2", "μάθημα 1", "Μάθημα 3", "Μάθημα 20"}
			for i, title := range titles {
				m := Meta{ID: fmt.Sprintf("lesson-%d", i), Title: title, Category: "Μαθήματα", Created: "2026-10-01T00:00:00Z"}
				if i == 4 {
					m.AccessList = "private"
				}
				if err := s.Put(m); err != nil {
					t.Fatal(err)
				}
			}
			var got []string
			for page := 1; page <= 2; page++ {
				p, err := BrowseVideos(s, VideoQuery{Sort: "title", Page: page, PageSize: 2, Category: "Μαθήματα", Text: "ΜΆΘΗΜΑ"})
				if err != nil {
					t.Fatal(err)
				}
				if p.Matches != 4 || p.Total != 4 {
					t.Fatalf("visibility/counts: %+v", p)
				}
				for _, m := range p.Items {
					got = append(got, m.Title)
				}
			}
			want := []string{"μάθημα 1", "Μάθημα 2", "Μάθημα 3", "Μάθημα 10"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}
