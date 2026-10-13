package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidID(t *testing.T) {
	valid := []string{"abc", "ABC-123_yes", "1759939200", "a", "0123456789abcdef"}
	for _, id := range valid {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false, want true", id)
		}
	}
	invalid := []string{"", "a/b", "a b", "../x", "a.b", "é", "a+b", string(make([]byte, 65))}
	for i := range invalid {
		if i == len(invalid)-1 {
			// 65 x zero bytes, definitely invalid
			invalid[i] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}
		if ValidID(invalid[i]) {
			t.Errorf("ValidID(%q) = true, want false", invalid[i])
		}
	}
}

func TestPutGetRoundtrip(t *testing.T) {
	s := NewFS(t.TempDir())
	m := Meta{ID: "1759939200", Title: "Test", Desc: "desc", Category: "CAT", Original: "original.mp4", HLS: "index.m3u8", Poster: "poster.jpg", Status: "ready"}
	if err := s.Put(m); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != m.Title || got.Original != m.Original || got.Status != m.Status {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if got.Poster != "poster.jpg" || got.HLS != "index.m3u8" {
		t.Errorf("roundtrip lost HLS/Poster: %+v", got)
	}
	if got.Created == "" {
		t.Error("Created should be set by Put")
	}
	// meta.json exists (perm 0644 on Unix; on Windows Go maps it to 0666)
	fi, err := os.Stat(filepath.Join(s.Dir(m.ID), "meta.json"))
	if err != nil {
		t.Fatalf("meta.json missing: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o644 && perm != 0o666 {
		t.Errorf("meta.json perm = %o, want 644 (or 666 on Windows)", perm)
	}
}

func TestPutInvalidID(t *testing.T) {
	s := NewFS(t.TempDir())
	if err := s.Put(Meta{ID: "../evil", Title: "x"}); err == nil {
		t.Error("Put with invalid ID should fail")
	}
	if _, err := s.Get("../evil"); err == nil {
		t.Error("Get with invalid ID should fail")
	}
}

func TestGetNotFound(t *testing.T) {
	s := NewFS(t.TempDir())
	if _, err := s.Get("nope"); err == nil {
		t.Error("Get missing should fail")
	}
}

func TestListFilterAndSort(t *testing.T) {
	s := NewFS(t.TempDir())
	// Put older first, newer last; List must sort Created desc.
	// Created is set by Put when empty, so sleep-free trick: set explicitly.
	a := Meta{ID: "a1", Title: "Alpha football", Created: "2024-01-01T00:00:00Z"}
	b := Meta{ID: "b2", Title: "Beta", Desc: "basketball highlights", Created: "2024-06-01T00:00:00Z"}
	c := Meta{ID: "c3", Title: "Gamma", Category: "FOOTBALL", Created: "2025-01-01T00:00:00Z"}
	for _, m := range []Meta{a, b, c} {
		if err := s.Put(m); err != nil {
			t.Fatalf("Put %s: %v", m.ID, err)
		}
	}

	all, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List(\"\") = %d items, want 3", len(all))
	}
	if all[0].ID != "c3" || all[1].ID != "b2" || all[2].ID != "a1" {
		t.Errorf("List sort wrong (want c3,b2,a1): %v %v %v", all[0].ID, all[1].ID, all[2].ID)
	}

	foot, _ := s.List("football")
	if len(foot) != 2 {
		t.Errorf("List(football) = %d, want 2", len(foot))
	}
	// case-insensitive + trims spaces
	bask, _ := s.List("  BASKETBALL ")
	if len(bask) != 1 || bask[0].ID != "b2" {
		t.Errorf("List(basketball) wrong: %+v", bask)
	}
	none, _ := s.List("no-such-thing")
	if len(none) != 0 {
		t.Errorf("List(nomatch) = %d, want 0", len(none))
	}
}

func TestListMissingRoot(t *testing.T) {
	s := NewFS(filepath.Join(t.TempDir(), "does-not-exist"))
	out, err := s.List("")
	if err != nil || len(out) != 0 {
		t.Errorf("List on missing root = %v, %v; want empty, nil", out, err)
	}
}

func TestListSkipsBadEntries(t *testing.T) {
	dir := t.TempDir()
	s := NewFS(dir)
	// plain file at root is skipped; dir without meta.json is skipped
	_ = os.WriteFile(filepath.Join(dir, "notadir"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "nometa"), 0o755)
	_ = s.Put(Meta{ID: "ok1", Title: "ok"})
	out, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 || out[0].ID != "ok1" {
		t.Errorf("List should skip bad entries, got %+v", out)
	}
}

func TestDelete(t *testing.T) {
	s := NewFS(t.TempDir())
	_ = s.Put(Meta{ID: "del1", Title: "bye"})
	if err := s.Delete("del1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("del1"); err == nil {
		t.Error("Get after Delete should fail")
	}
	if _, err := os.Stat(s.Dir("del1")); !os.IsNotExist(err) {
		t.Error("video dir should be gone after Delete")
	}
	if err := s.Delete("../evil"); err == nil {
		t.Error("Delete with invalid ID should fail")
	}
	if err := s.Delete("never-existed"); err != nil {
		t.Errorf("Delete of missing id should not fail: %v", err)
	}
}
