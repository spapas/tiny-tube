package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Roles for local authorization. Viewing stays public; upload/edit/delete and
// administration require a logged-in user with the matching role. LDAP only
// authenticates identity — roles are always assigned locally.
const (
	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// Auth sources, mirroring the SHDE model.
const (
	AuthLocal = "local"
	AuthLDAP  = "ldap"
)

type User struct {
	Username     string `json:"username"`
	DisplayName  string `json:"display_name,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
	AuthSource   string `json:"auth_source"`
	Role         string `json:"role"`
	Active       bool   `json:"active"`
	CreatedAt    string `json:"created_at"`
	LastLoginAt  string `json:"last_login_at,omitempty"`
}

type Session struct {
	TokenHash string `json:"token_hash"`
	Username  string `json:"username"`
	ExpiresAt string `json:"expires_at"`
}

var validUsername = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// ValidUsername rejects path separators, traversal and dot-leading names so
// usernames stay safe as FS filenames and URL segments.
func ValidUsername(name string) bool {
	if !validUsername.MatchString(name) || strings.HasPrefix(name, ".") {
		return false
	}
	return true
}

func ValidRole(role string) bool {
	return role == RoleAdmin || role == RoleEditor || role == RoleViewer
}

func (u User) IsAdmin() bool   { return u.Role == RoleAdmin }
func (u User) CanEdit() bool   { return u.Role == RoleAdmin || u.Role == RoleEditor }
func (u User) CanUpload() bool { return u.CanEdit() }

// SessionExpired reports whether the session is past its sliding expiry.
// Malformed timestamps are treated as expired.
func (s Session) SessionExpired(now time.Time) bool {
	expires, err := time.Parse(time.RFC3339Nano, s.ExpiresAt)
	if err != nil {
		return true
	}
	return !expires.After(now)
}

// Auth storage. Both backends agree: missing records return os.ErrNotExist,
// invalid names return os.ErrInvalid, and expired sessions behave as missing.
type AuthStore interface {
	GetUser(username string) (User, error)
	PutUser(u User) error
	DeleteUser(username string) error
	ListUsers() ([]User, error)
	UserCount() (int, error)
	CreateSession(s Session) error
	GetSession(tokenHash string) (Session, error)
	DeleteSession(tokenHash string) error
	DeleteUserSessions(username string) error
	PruneSessions(now time.Time) error
}

// FS auth records live under Root/.auth. The dot-leading directory is never
// served as media (see publicMediaPath) and carries no meta.json, so video
// listing and legacy import skip it.
func (s *FS) authDir() string             { return filepath.Join(s.Root, ".auth") }
func (s *FS) usersDir() string            { return filepath.Join(s.authDir(), "users") }
func (s *FS) sessionsDir() string         { return filepath.Join(s.authDir(), "sessions") }
func (s *FS) userPath(name string) string { return filepath.Join(s.usersDir(), name+".json") }

func validTokenHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *FS) writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
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
	return os.Rename(name, path)
}

func (s *FS) GetUser(username string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var u User
	if !ValidUsername(username) {
		return u, os.ErrInvalid
	}
	b, err := os.ReadFile(s.userPath(username))
	if err != nil {
		return u, err
	}
	if err := json.Unmarshal(b, &u); err != nil {
		return u, err
	}
	if u.Username != username {
		return User{}, os.ErrNotExist
	}
	return u, nil
}

func (s *FS) PutUser(u User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidUsername(u.Username) || !ValidRole(u.Role) {
		return os.ErrInvalid
	}
	if u.AuthSource != AuthLocal && u.AuthSource != AuthLDAP {
		return os.ErrInvalid
	}
	if u.CreatedAt == "" {
		u.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return s.writeJSONFile(s.userPath(u.Username), u)
}

func (s *FS) DeleteUser(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidUsername(username) {
		return os.ErrInvalid
	}
	err := os.Remove(s.userPath(username))
	if os.IsNotExist(err) {
		return os.ErrNotExist
	}
	return err
}

func (s *FS) ListUsers() ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.usersDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []User
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !ValidUsername(name) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.usersDir(), e.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var u User
		if err := json.Unmarshal(b, &u); err != nil {
			return nil, err
		}
		if u.Username != name {
			continue
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

func (s *FS) UserCount() (int, error) {
	users, err := s.ListUsers()
	if err != nil {
		return 0, err
	}
	return len(users), nil
}

func (s *FS) CreateSession(sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validTokenHash(sess.TokenHash) || !ValidUsername(sess.Username) {
		return os.ErrInvalid
	}
	return s.writeJSONFile(filepath.Join(s.sessionsDir(), sess.TokenHash+".json"), sess)
}

func (s *FS) GetSession(tokenHash string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var sess Session
	if !validTokenHash(tokenHash) {
		return sess, os.ErrInvalid
	}
	b, err := os.ReadFile(filepath.Join(s.sessionsDir(), tokenHash+".json"))
	if err != nil {
		return sess, err
	}
	if err := json.Unmarshal(b, &sess); err != nil {
		return sess, err
	}
	if sess.TokenHash != tokenHash || sess.SessionExpired(time.Now()) {
		return Session{}, os.ErrNotExist
	}
	return sess, nil
}

func (s *FS) DeleteSession(tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validTokenHash(tokenHash) {
		return os.ErrInvalid
	}
	err := os.Remove(filepath.Join(s.sessionsDir(), tokenHash+".json"))
	if os.IsNotExist(err) {
		return os.ErrNotExist
	}
	return err
}

func (s *FS) DeleteUserSessions(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidUsername(username) {
		return os.ErrInvalid
	}
	entries, err := os.ReadDir(s.sessionsDir())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.sessionsDir(), e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		var sess Session
		if err := json.Unmarshal(b, &sess); err != nil {
			return err
		}
		if sess.Username == username {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (s *FS) PruneSessions(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.sessionsDir())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.sessionsDir(), e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		var sess Session
		if err := json.Unmarshal(b, &sess); err != nil {
			return err
		}
		if sess.SessionExpired(now) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
