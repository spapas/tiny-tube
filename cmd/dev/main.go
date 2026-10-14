// Command dev runs the Tiny Tube server with automatic rebuilds during development.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	pollInterval = 250 * time.Millisecond
	quietPeriod  = 600 * time.Millisecond
)

type fileState struct {
	modTime time.Time
	size    int64
}

type runningServer struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func main() {
	if _, err := os.Stat("go.mod"); err != nil {
		log.Fatal("start this command from the repository root")
	}

	goCommand, err := exec.LookPath("go")
	if err != nil {
		log.Fatal("could not find go on PATH")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tempDir, err := os.MkdirTemp("", "tiny-tube-dev-")
	if err != nil {
		log.Fatalf("create temporary build directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	log.Println("watching Go files, web assets, go.mod and .env; press Ctrl+C to stop")
	server, err := buildAndStart(ctx, goCommand, tempDir, 1)
	if err != nil {
		log.Fatalf("initial server build: %v", err)
	}

	previous, err := snapshot()
	if err != nil {
		stopServer(server)
		log.Fatalf("scan source files: %v", err)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var changedAt time.Time
	buildNumber := 1

	for {
		select {
		case <-ctx.Done():
			stopServer(server)
			return
		case <-ticker.C:
			current, err := snapshot()
			if err != nil {
				log.Printf("scan source files: %v", err)
				continue
			}
			if !sameSnapshot(previous, current) {
				previous = current
				changedAt = time.Now()
				continue
			}
			if changedAt.IsZero() || time.Since(changedAt) < quietPeriod {
				continue
			}

			changedAt = time.Time{}
			buildNumber++
			log.Println("changes detected; rebuilding server")
			next, err := buildServer(ctx, goCommand, tempDir, buildNumber)
			if err != nil {
				log.Printf("build failed; keeping current server running: %v", err)
				continue
			}
			stopServer(server)
			server, err = startServer(ctx, next)
			if err != nil {
				log.Printf("start rebuilt server: %v", err)
				continue
			}
			log.Println("server restarted")
		}
	}
}

func buildAndStart(ctx context.Context, goCommand, tempDir string, buildNumber int) (*runningServer, error) {
	path, err := buildServer(ctx, goCommand, tempDir, buildNumber)
	if err != nil {
		return nil, err
	}
	return startServer(ctx, path)
}

func buildServer(ctx context.Context, goCommand, tempDir string, buildNumber int) (string, error) {
	name := fmt.Sprintf("tiny-tube-%d", buildNumber)
	if os.PathSeparator == '\\' {
		name += ".exe"
	}
	path := filepath.Join(tempDir, name)
	cmd := exec.CommandContext(ctx, goCommand, "build", "-o", path, ".")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build .: %w", err)
	}
	return path, nil
}

func startServer(ctx context.Context, path string) (*runningServer, error) {
	cmd := exec.CommandContext(ctx, path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	server := &runningServer{cmd: cmd, done: make(chan struct{})}
	go func() {
		defer close(server.done)
		_ = cmd.Wait()
	}()
	return server, nil
}

func stopServer(server *runningServer) {
	if server == nil || server.cmd == nil || server.cmd.Process == nil {
		return
	}
	if err := server.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		log.Printf("stop server: %v", err)
	}
	<-server.done
}

func snapshot() (map[string]fileState, error) {
	files := make(map[string]fileState)
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != "." {
				switch entry.Name() {
				case ".git", "tmp", "videos", "vendor", "bin", "dist", "node_modules":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !watchedFile(path) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[path] = fileState{modTime: info.ModTime(), size: info.Size()}
		return nil
	})
	return files, err
}

// watchedFile reports whether a change to path must trigger a rebuild.
// Go test files are excluded because the built binary ignores them;
// runtime directories (videos, tmp) are skipped during the walk itself.
func watchedFile(path string) bool {
	if strings.HasSuffix(path, ".go") {
		return !strings.HasSuffix(path, "_test.go")
	}
	slash := filepath.ToSlash(path)
	if strings.HasPrefix(slash, "web/templates/") && strings.HasSuffix(path, ".html") {
		return true
	}
	if strings.HasPrefix(slash, "web/static/") {
		return true
	}
	return path == "go.mod" || path == "go.sum" || path == ".env"
}

func sameSnapshot(a, b map[string]fileState) bool {
	if len(a) != len(b) {
		return false
	}
	for path, state := range a {
		if b[path] != state {
			return false
		}
	}
	return true
}
