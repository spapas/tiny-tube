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
	Views      int64  `json:"views"`
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidID(id string) bool { return validID.MatchString(id) }

type Store interface {
	List(q string) ([]Meta, error)
	Get(id string) (Meta, error)
	Put(m Meta) error
	Delete(id string) error
	Dir(id string) string
	// IncrementViews atomically adds one view and returns the new total.
	IncrementViews(id string) (int64, error)
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
	// A metadata edit must not revert a concurrent IncrementViews: keep the
	// stored count and let IncrementViews own the views field. putLocked stays
	// the raw writer that view increments use.
	if existing, err := s.getLocked(m.ID); err == nil {
		m.Views = existing.Views
	}
	return s.putLocked(m)
}

func (s *FS) putLocked(m Meta) error {
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
	return s.getLocked(id)
}

func (s *FS) getLocked(id string) (Meta, error) {
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

// SortByViews orders newest-first metadata by view count instead, keeping
// chronological order as the tiebreak.
func SortByViews(out []Meta) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Views != out[j].Views {
			return out[i].Views > out[j].Views
		}
		ti, _ := time.Parse(time.RFC3339Nano, out[i].Created)
		tj, _ := time.Parse(time.RFC3339Nano, out[j].Created)
		if ti.Equal(tj) {
			return out[i].ID > out[j].ID
		}
		return ti.After(tj)
	})
}

// IncrementViews adds one view under the store lock so concurrent metadata
// edits cannot lose counts.
func (s *FS) IncrementViews(id string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.getLocked(id)
	if err != nil {
		return 0, err
	}
	m.Views++
	if err := s.putLocked(m); err != nil {
		return 0, err
	}
	return m.Views, nil
}
