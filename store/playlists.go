package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Playlists are operator-curated ordered video series. Items hold video IDs
// in playback order; visibility is decided per video at render time, so
// locked videos stay hidden without a key exactly like everywhere else.
type Playlist struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Created string   `json:"created"`
	Items   []string `json:"items"`
}

// MaxPlaylistItems bounds a single series.
const MaxPlaylistItems = 200

type PlaylistStore interface {
	PutPlaylist(p Playlist) error
	GetPlaylist(id string) (Playlist, error)
	DeletePlaylist(id string) error
	ListPlaylists() ([]Playlist, error)
}

var validPlaylistID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidPlaylistID keeps playlist identifiers safe for metadata and filenames.
func ValidPlaylistID(id string) bool { return validPlaylistID.MatchString(id) }

// ValidPlaylistName bounds the operator-chosen display name.
func ValidPlaylistName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && len(name) <= 100
}

// GeneratePlaylistID mints an unguessable playlist identifier.
func GeneratePlaylistID() (string, error) {
	return GenerateAccessListID()
}

func (s *FS) playlistsDir() string { return filepath.Join(s.Root, ".playlists") }

func (s *FS) playlistPath(id string) string { return filepath.Join(s.playlistsDir(), id+".json") }

func (s *FS) PutPlaylist(p Playlist) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidPlaylistID(p.ID) || !ValidPlaylistName(p.Name) {
		return os.ErrInvalid
	}
	if len(p.Items) > MaxPlaylistItems {
		return os.ErrInvalid
	}
	for _, item := range p.Items {
		if !ValidID(item) {
			return os.ErrInvalid
		}
	}
	if p.Created == "" {
		p.Created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	items := append([]string(nil), p.Items...)
	if items == nil {
		items = []string{}
	}
	p.Items = items
	return s.writeJSONFile(s.playlistPath(p.ID), p)
}

func (s *FS) GetPlaylist(id string) (Playlist, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var p Playlist
	if !ValidPlaylistID(id) {
		return p, os.ErrInvalid
	}
	b, err := os.ReadFile(s.playlistPath(id))
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	if p.ID != id {
		return Playlist{}, os.ErrNotExist
	}
	return p, nil
}

func (s *FS) DeletePlaylist(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidPlaylistID(id) {
		return os.ErrInvalid
	}
	if err := os.Remove(s.playlistPath(id)); err != nil {
		return err
	}
	return nil
}

func (s *FS) ListPlaylists() ([]Playlist, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.playlistsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Playlist
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.playlistsDir(), e.Name()))
		if err != nil {
			return nil, err
		}
		var p Playlist
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created < out[j].Created
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *SQLite) PutPlaylist(p Playlist) error {
	if !ValidPlaylistID(p.ID) || !ValidPlaylistName(p.Name) {
		return os.ErrInvalid
	}
	if len(p.Items) > MaxPlaylistItems {
		return os.ErrInvalid
	}
	for _, item := range p.Items {
		if !ValidID(item) {
			return os.ErrInvalid
		}
	}
	if p.Created == "" {
		p.Created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO playlists(id, name, created_at) VALUES(?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name`, p.ID, p.Name, p.Created); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM playlist_items WHERE playlist_id=?`, p.ID); err != nil {
		return err
	}
	for i, item := range p.Items {
		if _, err := tx.Exec(`INSERT INTO playlist_items(playlist_id, video_id, position) VALUES(?,?,?)`, p.ID, item, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) GetPlaylist(id string) (Playlist, error) {
	var p Playlist
	if !ValidPlaylistID(id) {
		return p, os.ErrInvalid
	}
	if err := s.db.QueryRow(`SELECT id, name, created_at FROM playlists WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.Created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, os.ErrNotExist
		}
		return p, err
	}
	rows, err := s.db.Query(`SELECT video_id FROM playlist_items WHERE playlist_id=? ORDER BY position`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	p.Items = []string{}
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			return p, err
		}
		p.Items = append(p.Items, item)
	}
	return p, rows.Err()
}

func (s *SQLite) DeletePlaylist(id string) error {
	if !ValidPlaylistID(id) {
		return os.ErrInvalid
	}
	res, err := s.db.Exec(`DELETE FROM playlists WHERE id=?`, id)
	if err != nil {
		return err
	}
	if aff, _ := res.RowsAffected(); aff == 0 {
		return os.ErrNotExist
	}
	_, err = s.db.Exec(`DELETE FROM playlist_items WHERE playlist_id=?`, id)
	return err
}

func (s *SQLite) ListPlaylists() ([]Playlist, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM playlists ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Playlist
	for rows.Next() {
		var p Playlist
		if err := rows.Scan(&p.ID, &p.Name, &p.Created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		items, err := s.playlistItems(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Items = items
	}
	return out, nil
}

func (s *SQLite) playlistItems(id string) ([]string, error) {
	rows, err := s.db.Query(`SELECT video_id FROM playlist_items WHERE playlist_id=? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []string{}
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
