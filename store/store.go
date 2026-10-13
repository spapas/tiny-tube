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

type Meta struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Desc     string `json:"desc"`
	Category string `json:"category"`
	Created  string `json:"created"`
	Original string `json:"original"`
	HLS      string `json:"hls"`
	Poster   string `json:"poster"`
	Status   string `json:"status"`
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidID(id string) bool { return validID.MatchString(id) }

type Store interface {
	List(q string) ([]Meta, error)
	Get(id string) (Meta, error)
	Put(m Meta) error
	Delete(id string) error
	Dir(id string) string
}

type FS struct{ Root string }

func NewFS(root string) *FS { return &FS{Root: root} }

func (s *FS) Dir(id string) string { return filepath.Join(s.Root, id) }

// Delete removes the whole video dir (meta.json + blobs).
func (s *FS) Delete(id string) error {
	if !ValidID(id) {
		return os.ErrInvalid
	}
	return os.RemoveAll(s.Dir(id))
}

func (s *FS) Put(m Meta) error {
	if !ValidID(m.ID) {
		return os.ErrInvalid
	}
	d := s.Dir(m.ID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	if m.Created == "" {
		m.Created = time.Now().Format(time.RFC3339)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(d, "meta.json"), b, 0o644)
}

func (s *FS) Get(id string) (Meta, error) {
	var m Meta
	if !ValidID(id) {
		return m, os.ErrInvalid
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(id), "meta.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

func (s *FS) List(q string) ([]Meta, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := s.Get(e.Name())
		if err != nil {
			continue
		}
		if q != "" {
			hay := strings.ToLower(m.Title + " " + m.Desc + " " + m.Category)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out, nil
}
