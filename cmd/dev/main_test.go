package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeWatchFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotSkipsRuntimeAndBuildDirectories(t *testing.T) {
	t.Chdir(t.TempDir())
	writeWatchFile(t, "server.go", "package main")
	writeWatchFile(t, "store/store.go", "package store")
	writeWatchFile(t, filepath.Join("web", "templates", "list.html"), "<main>List</main>")
	writeWatchFile(t, filepath.Join("web", "static", "app.css"), "body {}")
	writeWatchFile(t, filepath.Join("web", "locales", "en.json"), `{"Title":"Title"}`)
	writeWatchFile(t, "go.mod", "module tiny-tube")
	writeWatchFile(t, ".env", "UPLOAD_PASS=secret")
	for _, directory := range []string{".git", "tmp", "videos", "vendor", "bin", "dist", "node_modules"} {
		writeWatchFile(t, filepath.Join(directory, "nested", "generated.go"), "package generated")
	}
	writeWatchFile(t, "server_test.go", "package main")
	writeWatchFile(t, filepath.Join("web", "other", "notes.html"), "<p>Not embedded</p>")
	files, err := snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 7 {
		t.Fatalf("watched files = %v, want only server sources, web assets, go.mod and .env", files)
	}
	for _, path := range []string{
		"server.go",
		filepath.Join("store", "store.go"),
		filepath.Join("web", "templates", "list.html"),
		filepath.Join("web", "static", "app.css"),
		filepath.Join("web", "locales", "en.json"),
		"go.mod",
		".env",
	} {
		if _, ok := files[path]; !ok {
			t.Errorf("missing watched file %s", path)
		}
	}
	writeWatchFile(t, filepath.Join("videos", "new-upload.mp4"), "binary")
	writeWatchFile(t, filepath.Join("tmp", "new-generated.go"), "package temporary")
	current, err := snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !sameSnapshot(files, current) {
		t.Fatal("runtime and temporary files must not trigger rebuilds")
	}
}

func TestSnapshotDetectsSourceAndAssetChanges(t *testing.T) {
	for _, path := range []string{
		"server.go",
		filepath.Join("web", "templates", "list.html"),
		filepath.Join("web", "static", "app.css"),
		filepath.Join("web", "locales", "en.json"),
		".env",
	} {
		t.Run(path, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeWatchFile(t, path, "initial")
			before, err := snapshot()
			if err != nil {
				t.Fatal(err)
			}
			writeWatchFile(t, path, "changed contents")
			after, err := snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if sameSnapshot(before, after) {
				t.Fatal("source/asset changes must trigger rebuilds")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			afterDelete, err := snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if sameSnapshot(after, afterDelete) {
				t.Fatal("source/asset deletion must trigger rebuilds")
			}
		})
	}
}

func TestSnapshotIgnoresTestFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	writeWatchFile(t, "server.go", "package main")
	before, err := snapshot()
	if err != nil {
		t.Fatal(err)
	}
	writeWatchFile(t, "server_test.go", "package main")
	after, err := snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !sameSnapshot(before, after) {
		t.Fatal("test files must not trigger rebuilds")
	}
}
