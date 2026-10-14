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
  error TEXT NOT NULL DEFAULT ''
);
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
	hasError := false
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == "error" {
			hasError = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !hasError {
		if _, err = db.Exec(`ALTER TABLE videos ADD COLUMN error TEXT NOT NULL DEFAULT ''`); err != nil {
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
	if m.Created == "" {
		m.Created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`INSERT INTO videos(id,title,description,category,created,original,hls,poster,status,error)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title=excluded.title,description=excluded.description,
			category=excluded.category,created=excluded.created,original=excluded.original,
			hls=excluded.hls,poster=excluded.poster,status=excluded.status,error=excluded.error`,
		m.ID, m.Title, m.Desc, m.Category, m.Created, m.Original, m.HLS, m.Poster, m.Status, m.Error)
	return err
}

func (s *SQLite) Get(id string) (Meta, error) {
	var m Meta
	if !ValidID(id) {
		return m, os.ErrInvalid
	}
	err := s.db.QueryRow(`SELECT id,title,description,category,created,original,hls,poster,status,error
		FROM videos WHERE id=?`, id).
		Scan(&m.ID, &m.Title, &m.Desc, &m.Category, &m.Created, &m.Original, &m.HLS, &m.Poster, &m.Status, &m.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return m, os.ErrNotExist
	}
	return m, err
}

func (s *SQLite) List(q string) ([]Meta, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	rows, err := s.db.Query(`SELECT id,title,description,category,created,original,hls,poster,status,error FROM videos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Meta
	for rows.Next() {
		var m Meta
		if err := rows.Scan(&m.ID, &m.Title, &m.Desc, &m.Category, &m.Created, &m.Original, &m.HLS, &m.Poster, &m.Status, &m.Error); err != nil {
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
		res, err := s.db.Exec(`INSERT INTO videos(id,title,description,category,created,original,hls,poster,status,error)
			VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
			m.ID, m.Title, m.Desc, m.Category, m.Created, m.Original, m.HLS, m.Poster, m.Status, m.Error)
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
