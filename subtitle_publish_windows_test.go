package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsSubtitleFallbackPreservesExistingTrackOnError(t *testing.T) {
	a := testApplication(t)
	id := uploadWithAccessList(t, a, "Training", "", "")
	video, err := a.mediaRoot.OpenRoot(id)
	if err != nil {
		t.Fatal(err)
	}
	defer video.Close()
	if err := video.Mkdir(subtitleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := video.OpenRoot(subtitleDir)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	const track = "track-en.vtt"
	if err := dir.WriteFile(track, []byte(sampleVTT), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceSubtitleOnWindows(video, dir, ".missing", track); err == nil {
		t.Fatal("missing staged file was published")
	}
	contents, err := dir.ReadFile(track)
	if err != nil || string(contents) != sampleVTT {
		t.Fatalf("failed replacement changed the original: %q, %v", contents, err)
	}
}

func TestWindowsSubtitleFallbackRejectsSubstitutedDirectory(t *testing.T) {
	a := testApplication(t)
	id := uploadWithAccessList(t, a, "Training", "", "")
	video, err := a.mediaRoot.OpenRoot(id)
	if err != nil {
		t.Fatal(err)
	}
	defer video.Close()
	if err := video.Mkdir(subtitleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := video.OpenRoot(subtitleDir)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := os.Rename(dir.Name(), dir.Name()+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir.Name(), 0o755); err != nil {
		t.Fatal(err)
	}
	const staging, track = ".upload-test", "track-en.vtt"
	staged := filepath.Join(dir.Name(), staging)
	if err := os.WriteFile(staged, []byte(sampleVTT), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceSubtitleOnWindows(video, dir, staging, track); err == nil {
		t.Fatal("fallback accepted a substituted directory")
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("fallback touched the substituted directory: %v", err)
	}
}
