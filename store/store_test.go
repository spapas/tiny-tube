package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentMetadataReplacement(t *testing.T) {
	s := NewFS(t.TempDir())
	m := Meta{ID: "video", Title: "initial", Original: "original.mp4", Status: "processing", Created: "2026-10-14T00:00:00Z"}
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				got, err := s.Get("video")
				if err != nil {
					t.Errorf("read during replacement: %v", err)
					return
				}
				if got.ID != "video" || got.Title == "" || got.Created != m.Created {
					t.Errorf("incomplete metadata: %+v", got)
					return
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		update := m
		update.Title = fmt.Sprintf("update %d", i)
		if err := s.Put(update); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	files, err := filepath.Glob(filepath.Join(s.Dir("video"), ".meta-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary metadata left behind: %v, %v", files, err)
	}
}

func TestStoreRejectsTraversalAndReportsCorruption(t *testing.T) {
	s := NewFS(t.TempDir())
	for _, id := range []string{"", "../escape", `..\escape`, "a/b"} {
		if err := s.Put(Meta{ID: id}); !errors.Is(err, os.ErrInvalid) {
			t.Errorf("Put(%q): %v", id, err)
		}
		if _, err := s.Get(id); !errors.Is(err, os.ErrInvalid) {
			t.Errorf("Get(%q): %v", id, err)
		}
	}
	if err := s.Put(Meta{ID: "bad"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir("bad"), "meta.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(""); err == nil {
		t.Fatal("corrupt metadata silently hidden")
	}
}

func TestMissingStorageRootReturnsError(t *testing.T) {
	s := NewFS(filepath.Join(t.TempDir(), "missing"))
	if _, err := s.List(""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing storage hidden: %v", err)
	}
}

func TestFailedMetadataReplacementCleansTemporaryFile(t *testing.T) {
	s := NewFS(t.TempDir())
	if err := os.MkdirAll(filepath.Join(s.Dir("video"), "meta.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Meta{ID: "video"}); err == nil {
		t.Fatal("replaced a directory with metadata")
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir("video"), ".meta-*"))
	if len(files) != 0 {
		t.Fatalf("temporary metadata left behind: %v", files)
	}
}

func TestSearchAndTimestampOrdering(t *testing.T) {
	s := NewFS(t.TempDir())
	for _, m := range []Meta{
		{ID: "older", Title: "Training", Category: "OPS", Created: "2026-10-14T09:00:00+03:00"},
		{ID: "newer", Title: "Training", Desc: "Example", Created: "2026-10-14T07:00:00Z"},
	} {
		if err := s.Put(m); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.List(" TRAINING ")
	if err != nil || len(items) != 2 || items[0].ID != "newer" {
		t.Fatalf("ordering: %+v, %v", items, err)
	}
	items, err = s.List("ops")
	if err != nil || len(items) != 1 || items[0].ID != "older" {
		t.Fatalf("category search: %+v, %v", items, err)
	}
}
