package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLite is a Store backed by a SQLite file. Video blobs (original, HLS
// segments, poster) stay on disk under Root/{id}/ — only the metadata
// (previously meta.json) lives in the database.
type SQLite struct {
	Root string
	db   *sql.DB
}

const sqliteSchema = `CREATE TABLE IF NOT EXISTS videos (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT '',
  created TEXT NOT NULL DEFAULT '',
  original TEXT NOT NULL DEFAULT '',
  hls TEXT NOT NULL DEFAULT '',
  poster TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  access_list TEXT NOT NULL DEFAULT '',
  views INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS access_lists (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  prefix TEXT NOT NULL DEFAULT '',
  key_hash TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_access_lists_prefix ON access_lists(prefix);
CREATE TABLE IF NOT EXISTS users (
  username TEXT PRIMARY KEY,
  display_name TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  auth_source TEXT NOT NULL DEFAULT 'local',
  role TEXT NOT NULL DEFAULT 'viewer',
  active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT '',
  last_login_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  username TEXT NOT NULL,
  expires_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS api_keys (
  prefix TEXT PRIMARY KEY,
  username TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  key_hash TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT '',
  last_used_at TEXT NOT NULL DEFAULT ''
)`

func NewSQLite(root, path string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Every operation uses the connection configured below, including busy_timeout.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA synchronous=NORMAL`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateSQLite(db); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLite{Root: root, db: db}, nil
}

// Preserve databases created before conversion error messages were persisted.
func migrateSQLite(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(videos)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !columns["error"] {
		if _, err = db.Exec(`ALTER TABLE videos ADD COLUMN error TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// Preserve databases created before locked lists existed.
	if !columns["access_list"] {
		if _, err = db.Exec(`ALTER TABLE videos ADD COLUMN access_list TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// Preserve databases created before view counts existed.
	if !columns["views"] {
		if _, err = db.Exec(`ALTER TABLE videos ADD COLUMN views INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	// Preserve databases created before user display names were persisted.
	if _, err = db.Exec(`ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	return nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) Dir(id string) string { return filepath.Join(s.Root, id) }

// Delete removes media before the row and reports failures from either step.
// Filesystem and database writes are not one transaction: a storage failure can
// leave an incomplete deletion that requires operator attention.
func (s *SQLite) Delete(id string) error {
	if !ValidID(id) {
		return os.ErrInvalid
	}
	if err := removeAllRetry(s.Dir(id)); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM videos WHERE id=?`, id)
	return err
}

func (s *SQLite) Put(m Meta) error {
	if !ValidID(m.ID) {
		return os.ErrInvalid
	}
	if m.AccessList != "" && !ValidAccessListID(m.AccessList) {
		return os.ErrInvalid
	}
	if m.Created == "" {
		m.Created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	// A metadata edit must not revert a concurrent IncrementViews: the views
	// column is owned by IncrementViews (a dedicated atomic UPDATE) and is only
	// seeded here when a row is first inserted.
	_, err := s.db.Exec(`INSERT INTO videos(id,title,description,category,created,original,hls,poster,status,error,access_list,views)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title=excluded.title,description=excluded.description,
			category=excluded.category,created=excluded.created,original=excluded.original,
			hls=excluded.hls,poster=excluded.poster,status=excluded.status,error=excluded.error,
			access_list=excluded.access_list`,
		m.ID, m.Title, m.Desc, m.Category, m.Created, m.Original, m.HLS, m.Poster, m.Status, m.Error, m.AccessList, m.Views)
	return err
}

func (s *SQLite) Get(id string) (Meta, error) {
	var m Meta
	if !ValidID(id) {
		return m, os.ErrInvalid
	}
	err := s.db.QueryRow(`SELECT id,title,description,category,created,original,hls,poster,status,error,access_list,views
		FROM videos WHERE id=?`, id).
		Scan(&m.ID, &m.Title, &m.Desc, &m.Category, &m.Created, &m.Original, &m.HLS, &m.Poster, &m.Status, &m.Error, &m.AccessList, &m.Views)
	if errors.Is(err, sql.ErrNoRows) {
		return m, os.ErrNotExist
	}
	return m, err
}

func (s *SQLite) List(q string) ([]Meta, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	rows, err := s.db.Query(`SELECT id,title,description,category,created,original,hls,poster,status,error,access_list,views FROM videos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Meta
	for rows.Next() {
		var m Meta
		if err := rows.Scan(&m.ID, &m.Title, &m.Desc, &m.Category, &m.Created, &m.Original, &m.HLS, &m.Poster, &m.Status, &m.Error, &m.AccessList, &m.Views); err != nil {
			return nil, err
		}
		// Go's Unicode case folding matches the FS backend, including Greek text.
		if q == "" || strings.Contains(strings.ToLower(m.Title+" "+m.Desc+" "+m.Category), q) {
			out = append(out, m)
		}
	}
	sortMetadata(out)
	return out, rows.Err()
}

// ImportFS copies metadata from legacy meta.json files (FS layout) into the// database. Entries whose id already exists are left untouched, so it is safe
// to run on every boot. Returns the number of imported entries.
func ImportFS(s *SQLite, root string) (int, error) {
	fs := NewFS(root)
	metas, err := fs.List("")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range metas {
		res, err := s.db.Exec(`INSERT INTO videos(id,title,description,category,created,original,hls,poster,status,error,access_list,views)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
			m.ID, m.Title, m.Desc, m.Category, m.Created, m.Original, m.HLS, m.Poster, m.Status, m.Error, m.AccessList, m.Views)
		if err != nil {
			return n, err
		}
		if aff, _ := res.RowsAffected(); aff == 1 {
			n++
		}
	}
	return n, nil
}

func scanUser(row interface {
	Scan(dest ...any) error
}) (User, error) {
	var u User
	var active int
	err := row.Scan(&u.Username, &u.DisplayName, &u.PasswordHash, &u.AuthSource, &u.Role, &active, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		return u, err
	}
	u.Active = active != 0
	return u, nil
}

func (s *SQLite) GetUser(username string) (User, error) {
	var u User
	if !ValidUsername(username) {
		return u, os.ErrInvalid
	}
	u, err := scanUser(s.db.QueryRow(`SELECT username,display_name,password_hash,auth_source,role,active,created_at,last_login_at
		FROM users WHERE username=?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, os.ErrNotExist
	}
	return u, err
}

func (s *SQLite) PutUser(u User) error {
	if !ValidUsername(u.Username) || !ValidRole(u.Role) {
		return os.ErrInvalid
	}
	if u.AuthSource != AuthLocal && u.AuthSource != AuthLDAP {
		return os.ErrInvalid
	}
	if u.CreatedAt == "" {
		u.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	active := 0
	if u.Active {
		active = 1
	}
	_, err := s.db.Exec(`INSERT INTO users(username,display_name,password_hash,auth_source,role,active,created_at,last_login_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(username) DO UPDATE SET display_name=excluded.display_name,password_hash=excluded.password_hash,auth_source=excluded.auth_source,
			role=excluded.role,active=excluded.active,last_login_at=excluded.last_login_at`,
		u.Username, u.DisplayName, u.PasswordHash, u.AuthSource, u.Role, active, u.CreatedAt, u.LastLoginAt)
	return err
}

func (s *SQLite) DeleteUser(username string) error {
	if !ValidUsername(username) {
		return os.ErrInvalid
	}
	res, err := s.db.Exec(`DELETE FROM users WHERE username=?`, username)
	if err != nil {
		return err
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		return os.ErrNotExist
	}
	_, err = s.db.Exec(`DELETE FROM api_keys WHERE username=?`, username)
	return err
}

func (s *SQLite) CreateAPIKey(k APIKey) error {
	if !ValidKeyPrefix(k.Prefix) || !ValidUsername(k.Username) || !validAPIKeyName(k.Name) || !validTokenHash(k.KeyHash) {
		return os.ErrInvalid
	}
	if k.CreatedAt == "" {
		k.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`INSERT INTO api_keys(prefix,username,name,key_hash,created_at,last_used_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(prefix) DO UPDATE SET username=excluded.username,name=excluded.name,key_hash=excluded.key_hash,
			created_at=excluded.created_at,last_used_at=excluded.last_used_at`,
		k.Prefix, k.Username, strings.TrimSpace(k.Name), k.KeyHash, k.CreatedAt, k.LastUsedAt)
	return err
}

func scanAPIKey(row interface {
	Scan(dest ...any) error
}) (APIKey, error) {
	var k APIKey
	err := row.Scan(&k.Prefix, &k.Username, &k.Name, &k.KeyHash, &k.CreatedAt, &k.LastUsedAt)
	return k, err
}

func (s *SQLite) GetAPIKey(prefix string) (APIKey, error) {
	var k APIKey
	if !ValidKeyPrefix(prefix) {
		return k, os.ErrInvalid
	}
	k, err := scanAPIKey(s.db.QueryRow(`SELECT prefix,username,name,key_hash,created_at,last_used_at
		FROM api_keys WHERE prefix=?`, prefix))
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, os.ErrNotExist
	}
	return k, err
}

func (s *SQLite) ListAPIKeys() ([]APIKey, error) {
	rows, err := s.db.Query(`SELECT prefix,username,name,key_hash,created_at,last_used_at
		FROM api_keys ORDER BY username,prefix`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *SQLite) DeleteAPIKey(prefix string) error {
	if !ValidKeyPrefix(prefix) {
		return os.ErrInvalid
	}
	res, err := s.db.Exec(`DELETE FROM api_keys WHERE prefix=?`, prefix)
	if err != nil {
		return err
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		return os.ErrNotExist
	}
	return nil
}

func (s *SQLite) DeleteUserAPIKeys(username string) error {
	if !ValidUsername(username) {
		return os.ErrInvalid
	}
	_, err := s.db.Exec(`DELETE FROM api_keys WHERE username=?`, username)
	return err
}

func (s *SQLite) TouchAPIKey(prefix string, at time.Time) error {
	if !ValidKeyPrefix(prefix) {
		return os.ErrInvalid
	}
	res, err := s.db.Exec(`UPDATE api_keys SET last_used_at=? WHERE prefix=?`,
		at.UTC().Format(time.RFC3339Nano), prefix)
	if err != nil {
		return err
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		return os.ErrNotExist
	}
	return nil
}

func (s *SQLite) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT username,display_name,password_hash,auth_source,role,active,created_at,last_login_at
		FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var active int
		if err := rows.Scan(&u.Username, &u.DisplayName, &u.PasswordHash, &u.AuthSource, &u.Role, &active, &u.CreatedAt, &u.LastLoginAt); err != nil {
			return nil, err
		}
		u.Active = active != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *SQLite) UserCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *SQLite) CreateSession(sess Session) error {
	if !validTokenHash(sess.TokenHash) || !ValidUsername(sess.Username) {
		return os.ErrInvalid
	}
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash,username,expires_at) VALUES(?,?,?)
		ON CONFLICT(token_hash) DO UPDATE SET username=excluded.username,expires_at=excluded.expires_at`,
		sess.TokenHash, sess.Username, sess.ExpiresAt)
	return err
}

func (s *SQLite) GetSession(tokenHash string) (Session, error) {
	var sess Session
	if !validTokenHash(tokenHash) {
		return sess, os.ErrInvalid
	}
	err := s.db.QueryRow(`SELECT token_hash,username,expires_at FROM sessions WHERE token_hash=?`, tokenHash).
		Scan(&sess.TokenHash, &sess.Username, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, os.ErrNotExist
	}
	if err != nil {
		return Session{}, err
	}
	if sess.SessionExpired(time.Now()) {
		return Session{}, os.ErrNotExist
	}
	return sess, nil
}

func (s *SQLite) DeleteSession(tokenHash string) error {
	if !validTokenHash(tokenHash) {
		return os.ErrInvalid
	}
	res, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	if err != nil {
		return err
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		return os.ErrNotExist
	}
	return nil
}

func (s *SQLite) DeleteUserSessions(username string) error {
	if !ValidUsername(username) {
		return os.ErrInvalid
	}
	_, err := s.db.Exec(`DELETE FROM sessions WHERE username=?`, username)
	return err
}

func (s *SQLite) PruneSessions(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now.UTC().Format(time.RFC3339Nano))
	return err
}

// IncrementViews atomically adds one view. The single connection serializes
// concurrent increments without losing counts.
func (s *SQLite) IncrementViews(id string) (int64, error) {
	if !ValidID(id) {
		return 0, os.ErrInvalid
	}
	res, err := s.db.Exec(`UPDATE videos SET views=views+1 WHERE id=?`, id)
	if err != nil {
		return 0, err
	}
	if aff, _ := res.RowsAffected(); aff == 0 {
		return 0, os.ErrNotExist
	}
	var views int64
	if err := s.db.QueryRow(`SELECT views FROM videos WHERE id=?`, id).Scan(&views); err != nil {
		return 0, err
	}
	return views, nil
}

func (s *SQLite) GetAccessList(id string) (AccessList, error) {
	var l AccessList
	if !ValidAccessListID(id) {
		return l, os.ErrInvalid
	}
	err := s.db.QueryRow(`SELECT id,name,prefix,key_hash,created_at FROM access_lists WHERE id=?`, id).
		Scan(&l.ID, &l.Name, &l.Prefix, &l.KeyHash, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessList{}, os.ErrNotExist
	}
	return l, err
}

func (s *SQLite) GetAccessListByPrefix(prefix string) (AccessList, error) {
	var l AccessList
	if !ValidKeyPrefix(prefix) {
		return l, os.ErrInvalid
	}
	err := s.db.QueryRow(`SELECT id,name,prefix,key_hash,created_at FROM access_lists WHERE prefix=?`, prefix).
		Scan(&l.ID, &l.Name, &l.Prefix, &l.KeyHash, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessList{}, os.ErrNotExist
	}
	return l, err
}

func (s *SQLite) ListAccessLists() ([]AccessList, error) {
	rows, err := s.db.Query(`SELECT id,name,prefix,key_hash,created_at FROM access_lists ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessList
	for rows.Next() {
		var l AccessList
		if err := rows.Scan(&l.ID, &l.Name, &l.Prefix, &l.KeyHash, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *SQLite) PutAccessList(l AccessList) error {
	if !ValidAccessListID(l.ID) || !ValidAccessListName(l.Name) ||
		!ValidKeyPrefix(l.Prefix) || !validTokenHash(l.KeyHash) {
		return os.ErrInvalid
	}
	l.Name = strings.TrimSpace(l.Name)
	if l.CreatedAt == "" {
		l.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`INSERT INTO access_lists(id,name,prefix,key_hash,created_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,prefix=excluded.prefix,
			key_hash=excluded.key_hash`,
		l.ID, l.Name, l.Prefix, l.KeyHash, l.CreatedAt)
	return err
}

func (s *SQLite) DeleteAccessList(id string) error {
	if !ValidAccessListID(id) {
		return os.ErrInvalid
	}
	res, err := s.db.Exec(`DELETE FROM access_lists WHERE id=?`, id)
	if err != nil {
		return err
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		return os.ErrNotExist
	}
	return nil
}
