package store

import (
	"reflect"
	"testing"
)

func testViewStores(t *testing.T) map[string]Store {
	t.Helper()
	return map[string]Store{"fs": NewFS(t.TempDir()), "sqlite": newTestSQLite(t)}
}

func recordDays(t *testing.T, name string, s Store, id string, days ...string) {
	t.Helper()
	var err error
	switch st := s.(type) {
	case *FS:
		for _, day := range days {
			if err = st.recordViewDay(id, day); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
	case *SQLite:
		for _, day := range days {
			if err = st.recordViewDay(id, day); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
	default:
		t.Fatalf("%s: unknown store %T", name, s)
	}
}

func TestViewHistoryNewestFirst(t *testing.T) {
	for name, s := range testViewStores(t) {
		recordDays(t, name, s, "vid", "2026-11-10", "2026-11-10", "2026-11-12")
		got, err := s.ViewHistory("vid", 10)
		if err != nil {
			t.Fatal(err)
		}
		want := []DayViews{{Day: "2026-11-12", Views: 1}, {Day: "2026-11-10", Views: 2}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %+v", name, got)
		}
		one, err := s.ViewHistory("vid", 1)
		if err != nil || len(one) != 1 || one[0].Day != "2026-11-12" {
			t.Fatalf("%s: limit: %+v %v", name, one, err)
		}
		if _, err := s.ViewHistory("../escape", 10); err == nil {
			t.Fatalf("%s: invalid id accepted", name)
		}
		empty, err := s.ViewHistory("missing", 10)
		if err != nil || len(empty) != 0 {
			t.Fatalf("%s: missing video: %+v %v", name, empty, err)
		}
	}
}

func TestIncrementViewsRecordsToday(t *testing.T) {
	for name, s := range testViewStores(t) {
		if err := s.Put(Meta{ID: "vid", Title: "t", Status: "ready"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.IncrementViews("vid"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.IncrementViews("vid"); err != nil {
			t.Fatal(err)
		}
		got, err := s.ViewHistory("vid", 1)
		if err != nil || len(got) != 1 || got[0].Views != 2 || got[0].Day != todayUTC() {
			t.Fatalf("%s: %+v %v", name, got, err)
		}
		if err := s.Delete("vid"); err != nil {
			t.Fatal(err)
		}
		after, err := s.ViewHistory("vid", 10)
		if err != nil || len(after) != 0 {
			t.Fatalf("%s: deleted video kept stats: %+v %v", name, after, err)
		}
	}
}
