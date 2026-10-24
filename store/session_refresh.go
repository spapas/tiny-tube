package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// RefreshSession only updates an existing live token, never inserts one.
// Comparing the expiry also coalesces simultaneous refreshes without reverting
// a newer expiry. Deletion and this operation share the same store lock.
func (s *FS) RefreshSession(previous Session, expiresAt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validTokenHash(previous.TokenHash) || !ValidUsername(previous.Username) || (Session{ExpiresAt: expiresAt}).SessionExpired(time.Now()) {
		return os.ErrInvalid
	}
	if previous.SessionExpired(time.Now()) {
		return os.ErrNotExist
	}
	path := filepath.Join(s.sessionsDir(), previous.TokenHash+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var current Session
	if err = json.Unmarshal(data, &current); err != nil {
		return err
	}
	if current.TokenHash != previous.TokenHash || current.Username != previous.Username || current.SessionExpired(time.Now()) {
		return os.ErrNotExist
	}
	if current.ExpiresAt != previous.ExpiresAt {
		return nil
	}
	current.ExpiresAt = expiresAt
	return s.writeJSONFile(path, current)
}

func (s *SQLite) RefreshSession(previous Session, expiresAt string) error {
	if !validTokenHash(previous.TokenHash) || !ValidUsername(previous.Username) || (Session{ExpiresAt: expiresAt}).SessionExpired(time.Now()) {
		return os.ErrInvalid
	}
	if previous.SessionExpired(time.Now()) {
		return os.ErrNotExist
	}
	result, err := s.db.Exec(`UPDATE sessions SET expires_at=? WHERE token_hash=? AND username=? AND expires_at=?`, expiresAt, previous.TokenHash, previous.Username, previous.ExpiresAt)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	// Another validator may already have refreshed it. A revoked/missing token
	// remains missing; this read never recreates it.
	current, err := s.GetSession(previous.TokenHash)
	if err != nil {
		return err
	}
	if current.Username != previous.Username {
		return os.ErrNotExist
	}
	return nil
}

// Login bookkeeping owns only its timestamp, never stale role/password data.
func (s *FS) TouchUserLogin(username string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidUsername(username) {
		return os.ErrInvalid
	}
	data, err := os.ReadFile(s.userPath(username))
	if err != nil {
		return err
	}
	var u User
	if err = json.Unmarshal(data, &u); err != nil {
		return err
	}
	if u.Username != username {
		return os.ErrNotExist
	}
	u.LastLoginAt = at.UTC().Format(time.RFC3339Nano)
	return s.writeJSONFile(s.userPath(username), u)
}

func (s *SQLite) TouchUserLogin(username string, at time.Time) error {
	if !ValidUsername(username) {
		return os.ErrInvalid
	}
	result, err := s.db.Exec(`UPDATE users SET last_login_at=? WHERE username=?`, at.UTC().Format(time.RFC3339Nano), username)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return os.ErrNotExist
	}
	return nil
}
