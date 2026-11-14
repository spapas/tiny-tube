package store

import (
	"reflect"
	"testing"
)

func TestMultiTokenSearchAgreesAcrossStores(t *testing.T) {
	stores := map[string]Store{"fs": NewFS(t.TempDir()), "sqlite": newTestSQLite(t)}
	for _, s := range stores {
		for _, m := range []Meta{
			{ID: "a", Title: "Learn Go workshop", Desc: "training", Created: "2026-11-01T10:00:00Z", Status: "ready"},
			{ID: "b", Title: "Go basics", Desc: "workshop intro", Created: "2026-11-02T10:00:00Z", Status: "ready"},
			{ID: "c", Title: "Unrelated", Created: "2026-11-03T10:00:00Z", Status: "ready"},
		} {
			if err := s.Put(m); err != nil {
				t.Fatal(err)
			}
		}
	}
	queries := []struct {
		text string
		want []string
	}{
		{"go learn", []string{"a"}},
		{"  GO   LEARN  ", []string{"a"}},
		{"workshop", []string{"a", "b"}},
		{"learn xyz", nil},
		{"", []string{"a", "b", "c"}},
	}
	ids := func(metas []Meta) []string {
		var out []string
		for _, m := range metas {
			out = append(out, m.ID)
		}
		return out
	}
	for _, q := range queries {
		fsPage, err := BrowseVideos(stores["fs"], VideoQuery{Text: q.text, All: true, PageSize: 100})
		if err != nil {
			t.Fatal(err)
		}
		dbPage, err := BrowseVideos(stores["sqlite"], VideoQuery{Text: q.text, All: true, PageSize: 100})
		if err != nil {
			t.Fatal(err)
		}
		// Newest first: c, b, a.
		if got := ids(fsPage.Items); !reflect.DeepEqual(got, reverse(q.want)) {
			t.Fatalf("fs %q: %v", q.text, got)
		}
		if got, want := ids(dbPage.Items), ids(fsPage.Items); !reflect.DeepEqual(got, want) {
			t.Fatalf("sqlite %q diverged: %v vs %v", q.text, got, want)
		}
		fsList, err := stores["fs"].List(q.text)
		if err != nil {
			t.Fatal(err)
		}
		dbList, err := stores["sqlite"].List(q.text)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := ids(dbList), ids(fsList); !reflect.DeepEqual(got, want) {
			t.Fatalf("List %q diverged: %v vs %v", q.text, got, want)
		}
	}
}

func reverse(in []string) []string {
	out := append([]string(nil), in...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
