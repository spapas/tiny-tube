package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAccessListStores(t *testing.T) map[string]Store {
	t.Helper()
	root := t.TempDir()
	db, err := NewSQLite(root, filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return map[string]Store{
		"fs":     NewFS(filepath.Join(root, "fsroot")),
		"sqlite": db,
	}
}

func TestAccessListKeyFormat(t *testing.T) {
	raw, prefix, hash, err := GenerateAccessKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "lt-") {
		t.Fatalf("missing prefix: %q", raw)
	}
	if got, ok := SplitAccessKey(raw); !ok || got != prefix {
		t.Fatalf("split: %q %v", got, ok)
	}
	if HashAccessKey(raw) != hash {
		t.Fatal("hash mismatch")
	}
	for _, bad := range []string{"", "tt-" + raw[3:], "lt-short", "lt-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if _, ok := SplitAccessKey(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestAccessListCRUD(t *testing.T) {
	for name, s := range testAccessListStores(t) {
		t.Run(name, func(t *testing.T) {
			id, err := GenerateAccessListID()
			if err != nil {
				t.Fatal(err)
			}
			raw, prefix, hash, err := GenerateAccessKey()
			if err != nil {
				t.Fatal(err)
			}
			_ = raw
			l := AccessList{ID: id, Name: "Έλεγχοι", Prefix: prefix, KeyHash: hash}
			if err := s.PutAccessList(l); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetAccessList(id)
			if err != nil || got.Name != "Έλεγχοι" || got.Prefix != prefix || got.KeyHash != hash {
				t.Fatalf("get: %+v %v", got, err)
			}
			byPrefix, err := s.GetAccessListByPrefix(prefix)
			if err != nil || byPrefix.ID != id {
				t.Fatalf("by prefix: %+v %v", byPrefix, err)
			}
			lists, err := s.ListAccessLists()
			if err != nil || len(lists) != 1 {
				t.Fatalf("list: %+v %v", lists, err)
			}
			// Rotation keeps the ID: videos stay assigned.
			_, prefix2, hash2, err := GenerateAccessKey()
			if err != nil {
				t.Fatal(err)
			}
			l.Prefix, l.KeyHash = prefix2, hash2
			if err := s.PutAccessList(l); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetAccessListByPrefix(prefix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old prefix still resolves: %v", err)
			}
			if err := s.DeleteAccessList(id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetAccessList(id); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("deleted list readable: %v", err)
			}
			if err := s.DeleteAccessList(id); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("second delete: %v", err)
			}
			if _, err := s.GetAccessList("no-such-list"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing list: %v", err)
			}
			if err := s.PutAccessList(AccessList{ID: "../x", Name: "bad", Prefix: prefix2, KeyHash: hash2}); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("bad id accepted: %v", err)
			}
		})
	}
}

func TestMetaAccessListRoundTrip(t *testing.T) {
	for name, s := range testAccessListStores(t) {
		t.Run(name, func(t *testing.T) {
			m := Meta{ID: "locked-vid", Title: "v", Original: "original.mp4", Status: "ready", AccessList: "list1"}
			if err := s.Put(m); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get("locked-vid")
			if err != nil || got.AccessList != "list1" {
				t.Fatalf("round trip: %+v %v", got, err)
			}
			if err := s.Put(Meta{ID: "bad", AccessList: "../escape"}); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("bad access list accepted: %v", err)
			}
		})
	}
}

func TestSQLiteMigratesExistingDatabaseWithAccessList(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.Replace(sqliteSchema, ",\n  access_list TEXT NOT NULL DEFAULT ''", "", 1)
	oldSchema = strings.Replace(oldSchema, "CREATE TABLE IF NOT EXISTS access_lists (", "CREATE TABLE IF NOT EXISTS dropped_lists (", 1)
	oldSchema = strings.Replace(oldSchema, "CREATE UNIQUE INDEX IF NOT EXISTS idx_access_lists_prefix ON access_lists(prefix);", "", 1)
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
	if err != nil || m.Title != "Keep me" || m.AccessList != "" {
		t.Fatalf("migration lost row: %+v %v", m, err)
	}
	m.AccessList = "list1"
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	if m, err = s.Get("existing"); err != nil || m.AccessList != "list1" {
		t.Fatalf("lost access list: %+v %v", m, err)
	}
	if _, err := s.ListAccessLists(); err != nil {
		t.Fatalf("access_lists table missing: %v", err)
	}
}
