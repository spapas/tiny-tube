package store

import (
	"database/sql"
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
  status TEXT NOT NULL DEFAULT ''
)`

func NewSQLite(root, path string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
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
	return &SQLite{Root: root, db: db}, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) Dir(id string) string { return filepath.Join(s.Root, id) }

// Delete removes the metadata row and the on-disk video dir (blobs included).
func (s *SQLite) Delete(id string) error {
	if !ValidID(id) {
		return os.ErrInvalid
	}
	if _, err := s.db.Exec(`DELETE FROM videos WHERE id=?`, id); err != nil {
		return err
	}
	return os.RemoveAll(s.Dir(id))
}

func (s *SQLite) Put(m Meta) error {
	if !ValidID(m.ID) {
		return os.ErrInvalid
	}
	if m.Created == "" {
		m.Created = time.Now().Format(time.RFC3339)
	}
	_, err := s.db.Exec(`INSERT INTO videos(id,title,description,category,created,original,hls,poster,status)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title=excluded.title,description=excluded.description,
			category=excluded.category,created=excluded.created,original=excluded.original,
			hls=excluded.hls,poster=excluded.poster,status=excluded.status`,
		m.ID, m.Title, m.Desc, m.Category, m.Created, m.Original, m.HLS, m.Poster, m.Status)
	return err
}

func (s *SQLite) Get(id string) (Meta, error) {
	var m Meta
	if !ValidID(id) {
		return m, os.ErrInvalid
	}
	err := s.db.QueryRow(`SELECT id,title,description,category,created,original,hls,poster,status
		FROM videos WHERE id=?`, id).
		Scan(&m.ID, &m.Title, &m.Desc, &m.Category, &m.Created, &m.Original, &m.HLS, &m.Poster, &m.Status)
	return m, err
}

func (s *SQLite) List(q string) ([]Meta, error) {
	q = strings.TrimSpace(q)
	var rows *sql.Rows
	var err error
	if q == "" {
		rows, err = s.db.Query(`SELECT id,title,description,category,created,original,hls,poster,status
			FROM videos ORDER BY created DESC`)
	} else {
		rows, err = s.db.Query(`SELECT id,title,description,category,created,original,hls,poster,status
			FROM videos WHERE instr(lower(title||' '||description||' '||category), lower(?)) > 0
			ORDER BY created DESC`, q)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Meta
	for rows.Next() {
		var m Meta
		if err := rows.Scan(&m.ID, &m.Title, &m.Desc, &m.Category, &m.Created, &m.Original, &m.HLS, &m.Poster, &m.Status); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ImportFS copies metadata from legacy meta.json files (FS layout) into the
// database. Entries whose id already exists are left untouched, so it is safe
// to run on every boot. Returns the number of imported entries.
func ImportFS(s *SQLite, root string) (int, error) {
	fs := NewFS(root)
	metas, err := fs.List("")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range metas {
		res, err := s.db.Exec(`INSERT INTO videos(id,title,description,category,created,original,hls,poster,status)
			VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
			m.ID, m.Title, m.Desc, m.Category, m.Created, m.Original, m.HLS, m.Poster, m.Status)
		if err != nil {
			return n, err
		}
		if aff, _ := res.RowsAffected(); aff == 1 {
			n++
		}
	}
	return n, nil
}
