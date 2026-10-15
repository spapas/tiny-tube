package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Meta struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Desc       string `json:"desc"`
	Category   string `json:"category"`
	Created    string `json:"created"`
	Original   string `json:"original"`
	HLS        string `json:"hls"`
	Poster     string `json:"poster"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	AccessList string `json:"access_list,omitempty"`
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidID(id string) bool { return validID.MatchString(id) }

type Store interface {
	List(q string) ([]Meta, error)
	Get(id string) (Meta, error)
	Put(m Meta) error
	Delete(id string) error
	Dir(id string) string
	AuthStore
	AccessListStore
}

type FS struct {
	Root string
	mu   sync.RWMutex
}

func NewFS(root string) *FS { return &FS{Root: root} }

func (s *FS) Dir(id string) string { return filepath.Join(s.Root, id) }

func (s *FS) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) {
		return os.ErrInvalid
	}
	return removeAllRetry(s.Dir(id))
}

// Windows can briefly retain media handles after cancellation of FFmpeg.
func removeAllRetry(dir string) error {
	var err error
	for attempt := 0; attempt < 30; attempt++ {
		if err = os.RemoveAll(dir); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

func (s *FS) Put(m Meta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(m.ID) {
		return os.ErrInvalid
	}
	if m.AccessList != "" && !ValidAccessListID(m.AccessList) {
		return os.ErrInvalid
	}
	d := s.Dir(m.ID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	if m.Created == "" {
		m.Created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(d, ".meta-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0o644); err != nil {
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
	return os.Rename(name, filepath.Join(d, "meta.json"))
}

func (s *FS) Get(id string) (Meta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var m Meta
	if !ValidID(id) {
		return m, os.ErrInvalid
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(id), "meta.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	if err == nil && m.ID != id {
		err = fmt.Errorf("metadata ID does not match directory %q", id)
	}
	return m, err
}

func (s *FS) List(q string) ([]Meta, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
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
			if os.IsNotExist(err) || !ValidID(e.Name()) {
				continue
			}
			return nil, fmt.Errorf("read metadata %s: %w", e.Name(), err)
		}
		if q != "" {
			hay := strings.ToLower(m.Title + " " + m.Desc + " " + m.Category)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, m)
	}
	sortMetadata(out)
	return out, nil
}

func sortMetadata(out []Meta) {
	sort.Slice(out, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339Nano, out[i].Created)
		tj, _ := time.Parse(time.RFC3339Nano, out[j].Created)
		if ti.Equal(tj) {
			return out[i].ID > out[j].ID
		}
		return ti.After(tj)
	})
}
