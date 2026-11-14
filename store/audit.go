package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

// AuditEntry records a single auditable action: who did what to which
// target and when. Times are UTC RFC3339Nano strings.
type AuditEntry struct {
	ID     int64  `json:"id"`
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail,omitempty"`
}

// AuditStore persists operator audit entries. Reads return newest first.
type AuditStore interface {
	AppendAudit(e AuditEntry) error
	ListAudit(limit int) ([]AuditEntry, error)
}

// Audit bounds shared by both backends: the log keeps the newest
// AuditKeepEntries rows and a single read returns at most AuditMaxLimit.
const (
	AuditKeepEntries = 5000
	AuditMaxLimit    = 500
	AuditPageSize    = 200
)

func clampAuditLimit(limit int) int {
	if limit <= 0 {
		return AuditPageSize
	}
	if limit > AuditMaxLimit {
		return AuditMaxLimit
	}
	return limit
}

func (s *FS) auditPath() string { return filepath.Join(s.authDir(), "audit.log") }

func (s *FS) AppendAudit(e AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.authDir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.auditPath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	e.ID = 0
	b, err := json.Marshal(e)
	if err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Small logs never need pruning; check the size before scanning.
	if fi, err := os.Stat(s.auditPath()); err != nil || fi.Size() <= 256*1024 {
		return err
	}
	return s.pruneAuditLocked()
}

func (s *FS) readAuditLocked() ([]AuditEntry, error) {
	f, err := os.Open(s.auditPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []AuditEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		e.ID = int64(len(out) + 1)
		out = append(out, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// pruneAuditLocked keeps only the newest AuditKeepEntries rows.
func (s *FS) pruneAuditLocked() error {
	entries, err := s.readAuditLocked()
	if err != nil {
		return err
	}
	if len(entries) <= AuditKeepEntries {
		return nil
	}
	entries = entries[len(entries)-AuditKeepEntries:]
	f, err := os.CreateTemp(s.authDir(), ".audit-*.log")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range entries {
		e.ID = 0
		b, err := json.Marshal(e)
		if err != nil {
			f.Close()
			return err
		}
		if _, err = w.Write(append(b, '\n')); err != nil {
			f.Close()
			return err
		}
	}
	if err = w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.auditPath())
}

func (s *FS) ListAudit(limit int) ([]AuditEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := s.readAuditLocked()
	if err != nil {
		return nil, err
	}
	limit = clampAuditLimit(limit)
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

func (s *SQLite) AppendAudit(e AuditEntry) error {
	if _, err := s.db.Exec(`INSERT INTO audit_log(time, actor, action, target, detail) VALUES (?,?,?,?,?)`,
		e.Time, e.Actor, e.Action, e.Target, e.Detail); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM audit_log WHERE id <= (SELECT COALESCE(MAX(id), 0) - ? FROM audit_log)`, AuditKeepEntries)
	return err
}

func (s *SQLite) ListAudit(limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(`SELECT id, time, actor, action, target, detail FROM audit_log ORDER BY id DESC LIMIT ?`, clampAuditLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.Actor, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
