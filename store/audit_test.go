package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func testAuditStores(t *testing.T) map[string]AuditStore {
	t.Helper()
	root := t.TempDir()
	sqlite, err := NewSQLite(root, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlite.Close() })
	return map[string]AuditStore{"fs": NewFS(t.TempDir()), "sqlite": sqlite}
}

func TestAuditAppendAndListNewestFirst(t *testing.T) {
	for name, s := range testAuditStores(t) {
		for i := 1; i <= 3; i++ {
			if err := s.AppendAudit(AuditEntry{
				Time: fmt.Sprintf("2026-11-15T10:00:0%dZ", i), Actor: "admin",
				Action: "video.upload", Target: fmt.Sprintf("id-%d", i),
			}); err != nil {
				t.Fatalf("%s: append: %v", name, err)
			}
		}
		got, err := s.ListAudit(10)
		if err != nil {
			t.Fatalf("%s: list: %v", name, err)
		}
		if len(got) != 3 || got[0].Target != "id-3" || got[2].Target != "id-1" {
			t.Fatalf("%s: newest-first order wrong: %+v", name, got)
		}
		if got[0].ID == got[1].ID {
			t.Fatalf("%s: entries need distinct IDs", name)
		}
		empty, err := s.ListAudit(0)
		if err != nil || len(empty) != 3 {
			t.Fatalf("%s: zero limit should return the page default: %v %d", name, err, len(empty))
		}
	}
}

func TestAuditListLimitClamped(t *testing.T) {
	for name, s := range testAuditStores(t) {
		for i := 0; i < 10; i++ {
			if err := s.AppendAudit(AuditEntry{Time: "2026-11-15T10:00:00Z", Actor: "a", Action: "video.edit", Target: "v"}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.ListAudit(4)
		if err != nil || len(got) != 4 {
			t.Fatalf("%s: expected 4 entries, got %d (%v)", name, len(got), err)
		}
		huge, err := s.ListAudit(AuditMaxLimit + 1000)
		if err != nil || len(huge) != 10 {
			t.Fatalf("%s: oversized limit should clamp, not fail: %d (%v)", name, len(huge), err)
		}
	}
}

func TestAuditLogStaysBounded(t *testing.T) {
	for name, s := range testAuditStores(t) {
		fillAudit(t, name, s, AuditKeepEntries+100)
		if err := s.AppendAudit(AuditEntry{Time: "2026-11-15T10:00:00Z", Actor: "a", Action: "video.edit", Target: "v"}); err != nil {
			t.Fatal(err)
		}
		if stored := countAudit(t, name, s); stored > AuditKeepEntries {
			t.Fatalf("%s: audit log grew past the bound: %d", name, stored)
		}
		got, err := s.ListAudit(AuditMaxLimit)
		if err != nil || len(got) != AuditMaxLimit {
			t.Fatalf("%s: expected a full clamped page, got %d (%v)", name, len(got), err)
		}
	}
}

// fillAudit pre-fills n entries without triggering pruning, so the bound
// itself is exercised by a single AppendAudit.
func fillAudit(t *testing.T, name string, s AuditStore, n int) {
	t.Helper()
	switch st := s.(type) {
	case *FS:
		if err := os.MkdirAll(st.authDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(st.auditPath(), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		w := bufio.NewWriter(f)
		for i := 0; i < n; i++ {
			b, err := json.Marshal(AuditEntry{Time: "2026-11-15T10:00:00Z", Actor: "a", Action: "video.edit", Target: "v"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(append(b, '\n')); err != nil {
				f.Close()
				t.Fatal(err)
			}
		}
		if err := w.Flush(); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	case *SQLite:
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			if _, err := tx.Exec(`INSERT INTO audit_log(time, actor, action, target) VALUES (?,?,?,?)`,
				"2026-11-15T10:00:00Z", "a", "video.edit", "v"); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("%s: unknown audit store %T", name, s)
	}
}

func countAudit(t *testing.T, name string, s AuditStore) int {
	t.Helper()
	switch st := s.(type) {
	case *FS:
		b, err := os.ReadFile(st.auditPath())
		if err != nil {
			t.Fatalf("%s: read audit log: %v", name, err)
		}
		return bytes.Count(b, []byte("\n"))
	case *SQLite:
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
			t.Fatalf("%s: count audit log: %v", name, err)
		}
		return n
	default:
		t.Fatalf("%s: unknown audit store %T", name, s)
		return 0
	}
}
