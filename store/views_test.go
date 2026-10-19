package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestIncrementViews(t *testing.T) {
	for name, s := range testAccessListStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := s.Put(Meta{ID: "vid", Title: "v", Original: "original.mp4", Status: "ready"}); err != nil {
				t.Fatal(err)
			}
			for want := int64(1); want <= 3; want++ {
				got, err := s.IncrementViews("vid")
				if err != nil || got != want {
					t.Fatalf("increment %d: %d %v", want, got, err)
				}
			}
			m, err := s.Get("vid")
			if err != nil || m.Views != 3 {
				t.Fatalf("persisted: %+v %v", m, err)
			}
			// Views survive a metadata edit round-trip.
			m.Title = "renamed"
			if err := s.Put(m); err != nil {
				t.Fatal(err)
			}
			if m, err = s.Get("vid"); err != nil || m.Views != 3 {
				t.Fatalf("edit lost views: %+v %v", m, err)
			}
			if _, err := s.IncrementViews("missing"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing: %v", err)
			}
			if _, err := s.IncrementViews("../escape"); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("invalid: %v", err)
			}
		})
	}
}

// TestPutPreservesConcurrentViews reproduces a lost update: an edit that read
// metadata before a view increment would otherwise write its stale count back
// and revert the increment. Put must leave the views field to IncrementViews.
func TestPutPreservesConcurrentViews(t *testing.T) {
	for name, s := range testAccessListStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := s.Put(Meta{ID: "vid", Title: "v", Original: "original.mp4", Status: "ready"}); err != nil {
				t.Fatal(err)
			}
			stale, err := s.Get("vid")
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err := s.IncrementViews("vid"); err != nil {
					t.Fatal(err)
				}
			}
			stale.Title = "edited after the increments"
			if err := s.Put(stale); err != nil {
				t.Fatal(err)
			}
			m, err := s.Get("vid")
			if err != nil || m.Views != 2 || m.Title != "edited after the increments" {
				t.Fatalf("stale Put reverted views: %+v %v", m, err)
			}
		})
	}
}

func TestIncrementViewsConcurrent(t *testing.T) {
	for name, s := range testAccessListStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := s.Put(Meta{ID: "hot", Title: "v", Status: "ready"}); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for g := 0; g < 8; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < 25; i++ {
						if _, err := s.IncrementViews("hot"); err != nil {
							t.Errorf("increment: %v", err)
							return
						}
					}
				}()
			}
			wg.Wait()
			m, err := s.Get("hot")
			if err != nil || m.Views != 200 {
				t.Fatalf("lost increments: %+v %v", m, err)
			}
		})
	}
}

func TestSortByViews(t *testing.T) {
	in := []Meta{
		{ID: "b", Views: 5, Created: "2026-10-14T00:00:00Z"},
		{ID: "a", Views: 9, Created: "2026-10-13T00:00:00Z"},
		{ID: "c", Views: 9, Created: "2026-10-15T00:00:00Z"},
	}
	SortByViews(in)
	var ids []string
	for _, m := range in {
		ids = append(ids, fmt.Sprintf("%s:%d", m.ID, m.Views))
	}
	if strings.Join(ids, ",") != "c:9,a:9,b:5" {
		t.Fatalf("order: %v", ids)
	}
}

func TestSQLiteMigratesExistingDatabaseWithViews(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.Replace(sqliteSchema, ",\n  views INTEGER NOT NULL DEFAULT 0", "", 1)
	if _, err := db.Exec(oldSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO videos(id,title,status) VALUES('existing','Keep me','ready')`); err != nil {
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
	if err != nil || m.Views != 0 {
		t.Fatalf("migration: %+v %v", m, err)
	}
	if total, err := s.IncrementViews("existing"); err != nil || total != 1 {
		t.Fatalf("increment after migration: %d %v", total, err)
	}
}
