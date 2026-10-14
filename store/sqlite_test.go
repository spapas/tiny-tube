package store

import (
	"path/filepath"
	"testing"
)

func newTestSQLite(t *testing.T) *SQLite {
	t.Helper()
	s, err := NewSQLite(t.TempDir(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLitePutGetRoundtrip(t *testing.T) {
	s := newTestSQLite(t)
	m := Meta{ID: "abc1", Title: "Τίτλος", Desc: "δοκιμή", Category: "CAT", Original: "original.mp4", HLS: "index.m3u8", Poster: "poster.jpg", Status: "ready"}
	if err := s.Put(m); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get("abc1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != m && (got.Title != m.Title || got.Desc != m.Desc || got.Category != m.Category ||
		got.Original != m.Original || got.HLS != m.HLS || got.Poster != m.Poster || got.Status != m.Status) {
		t.Errorf("roundtrip mismatch: %+v want %+v", got, m)
	}
	if got.Created == "" {
		t.Error("Created should be set by Put")
	}
	// overwrite same id (transcode finish path)
	m.Status = "processing"
	m.Title = "New title"
	if err := s.Put(m); err != nil {
		t.Fatalf("re-Put: %v", err)
	}
	got, _ = s.Get("abc1")
	if got.Title != "New title" || got.Status != "processing" {
		t.Errorf("re-Put did not update: %+v", got)
	}
}

func TestSQLiteInvalidID(t *testing.T) {
	s := newTestSQLite(t)
	if err := s.Put(Meta{ID: "../evil", Title: "x"}); err == nil {
		t.Error("Put invalid ID should fail")
	}
	if _, err := s.Get("../evil"); err == nil {
		t.Error("Get invalid ID should fail")
	}
	if _, err := s.Get("missing"); err == nil {
		t.Error("Get missing should fail")
	}
}

func TestSQLiteListFilterSort(t *testing.T) {
	s := newTestSQLite(t)
	for _, m := range []Meta{
		{ID: "a1", Title: "Alpha football", Created: "2024-01-01T00:00:00Z"},
		{ID: "b2", Title: "Beta", Desc: "basketball highlights", Created: "2024-06-01T00:00:00Z"},
		{ID: "c3", Title: "Gamma", Category: "FOOTBALL", Created: "2025-01-01T00:00:00Z"},
	} {
		if err := s.Put(m); err != nil {
			t.Fatalf("Put %s: %v", m.ID, err)
		}
	}
	all, _ := s.List("")
	if len(all) != 3 || all[0].ID != "c3" || all[1].ID != "b2" || all[2].ID != "a1" {
		t.Errorf("List sort wrong: %+v", all)
	}
	foot, _ := s.List("football")
	if len(foot) != 2 {
		t.Errorf("List(football) = %d, want 2", len(foot))
	}
	bask, _ := s.List("  BASKETBALL ")
	if len(bask) != 1 || bask[0].ID != "b2" {
		t.Errorf("List(basketball) wrong: %+v", bask)
	}
	// % and _ are literal with instr(), must not act as wildcards
	if res, _ := s.List("%"); len(res) != 0 {
		t.Errorf("List(%%) = %d, want 0", len(res))
	}
}

func TestSQLiteImportFS(t *testing.T) {
	root := t.TempDir()
	fs := NewFS(root)
	_ = fs.Put(Meta{ID: "old1", Title: "Legacy", Desc: "from json", Created: "2024-01-01T00:00:00Z", Original: "original.mp4", Status: "ready"})
	s, err := NewSQLite(root, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, err := ImportFS(s, root)
	if err != nil || n != 1 {
		t.Fatalf("ImportFS = %d, %v; want 1, nil", n, err)
	}
	got, err := s.Get("old1")
	if err != nil || got.Title != "Legacy" || got.Desc != "from json" {
		t.Errorf("imported wrong: %+v %v", got, err)
	}
	// second run imports nothing (idempotent)
	n, err = ImportFS(s, root)
	if err != nil || n != 0 {
		t.Errorf("re-import = %d, %v; want 0, nil", n, err)
	}
	// Dir still points at the on-disk video folder
	if d := s.Dir("old1"); d != filepath.Join(root, "old1") {
		t.Errorf("Dir = %q", d)
	}
}

func TestSQLiteDelete(t *testing.T) {
	s := newTestSQLite(t)
	_ = s.Put(Meta{ID: "del1", Title: "bye"})
	if err := s.Delete("del1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("del1"); err == nil {
		t.Error("Get after Delete should fail")
	}
	if got, _ := s.List(""); len(got) != 0 {
		t.Errorf("List after Delete = %d, want 0", len(got))
	}
	if err := s.Delete("../evil"); err == nil {
		t.Error("Delete with invalid ID should fail")
	}
}
