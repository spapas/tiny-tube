package main

import (
	"context"
	"errors"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

func init() {
	// HLS mime types missing on plain Windows Go installs
	_ = mime.AddExtensionType(".m3u8", "application/vnd.apple.mpegurl")
	_ = mime.AddExtensionType(".ts", "video/mp2t")
	_ = mime.AddExtensionType(".vtt", "text/vtt; charset=utf-8")
}

func main() {
	if err := requirePatchedGo(runtime.Version()); err != nil {
		log.Fatal(err)
	}
	loadDotEnv(".env")
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if os.Getenv("UPLOAD_USER") != "" || os.Getenv("UPLOAD_PASS") != "" {
		log.Printf("UPLOAD_USER/UPLOAD_PASS are obsolete; login uses ADMIN_USER/ADMIN_PASS and managed users")
	}
	a, err := newApplication(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := a.close(); err != nil {
			log.Printf("close metadata: %v", err)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.startWorkers(ctx)
	a.startBackupScheduler(ctx)
	server := a.httpServer()
	metrics := a.metricsServer()
	if metrics != nil {
		// Bind before serving so a bad METRICS_ADDR fails startup.
		ln, err := net.Listen("tcp", a.cfg.MetricsAddr)
		if err != nil {
			log.Fatalf("METRICS_ADDR: %v", err)
		}
		a.startMetricsRefresh(ctx)
		go func() {
			log.Printf("metrics on http://%s%s", ln.Addr(), metricsPath)
			if err := metrics.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("metrics server: %v", err)
			}
		}()
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if metrics != nil {
			_ = metrics.Shutdown(shutdownCtx)
		}
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP shutdown: %v", err)
			server.Close()
		}
	}()
	log.Printf("Tiny Tube on %s VIDEO_DIR=%s ffmpeg=%q", cfg.address(), a.videoDir, a.ffmpegBin)
	err = server.ListenAndServe()
	stop()
	<-shutdownDone
	a.workers.Wait()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
