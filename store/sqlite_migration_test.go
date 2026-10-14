package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteMigratesExistingDatabaseAndPersistsError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.Replace(sqliteSchema, ",\n  error TEXT NOT NULL DEFAULT ''", "", 1)
	if _, err := db.Exec(oldSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO videos(id,title,poster,status) VALUES('existing','Keep me','poster.jpg','ready')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := NewSQLite(root, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := s.Get("existing")
	if err != nil || m.Title != "Keep me" || m.Poster != "poster.jpg" {
		t.Fatalf("migration lost row: %+v %v", m, err)
	}
	m.Status, m.Error = "failed", "conversion failed"
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	m, err = s.Get("existing")
	if err != nil || m.Error != "conversion failed" {
		t.Fatalf("lost failure: %+v %v", m, err)
	}
	if _, err := s.Get("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing row error: %v", err)
	}
}

func TestSQLiteSortsInstantsAndSearchesGreek(t *testing.T) {
	s := newTestSQLite(t)
	for _, m := range []Meta{{ID: "later", Title: "ΟΜΑΔΑ", Created: "2024-01-01T00:30:00Z"}, {ID: "earlier", Title: "ΟΜΑΔΑ", Created: "2024-01-01T02:00:00+02:00"}} {
		if err := s.Put(m); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.List("ομαδα")
	if err != nil || len(items) != 2 || items[0].ID != "later" {
		t.Fatalf("search/order differs from FS: %+v %v", items, err)
	}
}
