package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tiny-tube/store"
)

// Resumable uploads split large files into server-staged chunks so an
// interrupted transfer continues where it stopped instead of restarting.
// The browser client uses this protocol; plain multipart POST /api/upload
// remains for no-JavaScript clients and scripts.
//
//	POST /api/uploads                {filename, size} -> {id, chunk_size}
//	GET  /api/uploads/{id}           -> {id, filename, size, received}
//	POST /api/uploads/{id}/chunk?offset=N   raw bytes -> {received}
//	POST /api/uploads/{id}/complete  multipart fields -> video, as /api/upload
//
// Completed uploads run the same validation, publication and conversion
// pipeline as single-shot uploads. Stale staging directories are removed on
// startup; staging bytes count toward VIDEO_DIR_QUOTA_BYTES while present.

const uploadChunkSize = 8 << 20

const maxStagingAge = 24 * time.Hour

type stagedUpload struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Created  string `json:"created"`
}

func stagingDir(root, id string) string {
	return filepath.Join(root, ".staging-"+id)
}

func validStagingID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

func reserveStagingDir(root string) (string, string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", "", err
		}
		id := hex.EncodeToString(random[:])
		dir := stagingDir(root, id)
		if err := os.Mkdir(dir, 0o755); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", "", err
		}
		return id, dir, nil
	}
	return "", "", fmt.Errorf("could not reserve unique staging directory")
}

func (a *application) readStaging(id string) (stagedUpload, string, error) {
	var meta stagedUpload
	if !validStagingID(id) {
		return meta, "", os.ErrInvalid
	}
	dir := stagingDir(a.videoDir, id)
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return meta, "", err
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return meta, "", err
	}
	return meta, dir, nil
}

func (a *application) handleUploadInit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "filename and size required"})
		return
	}
	req.Filename = strings.TrimSpace(req.Filename)
	ext := strings.ToLower(filepath.Ext(req.Filename))
	if ext != ".mp4" && ext != ".mov" && ext != ".mkv" && ext != ".webm" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "supported formats: mp4, mov, mkv, webm"})
		return
	}
	if req.Size <= 0 || req.Size > a.cfg.MaxUploadBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "upload exceeds MAX_UPLOAD_BYTES"})
		return
	}
	if a.cfg.QuotaBytes > 0 {
		exceeded, err := a.quotaExceeded(req.Size)
		if err != nil {
			a.apiServerError(w, r, "measure storage usage", err)
			return
		}
		if exceeded {
			writeAPIError(w, r, http.StatusRequestEntityTooLarge, "error.quotaExceeded")
			return
		}
	}
	id, dir, err := reserveStagingDir(a.videoDir)
	if err != nil {
		a.apiServerError(w, r, "reserve staging", err)
		return
	}
	meta, err := json.Marshal(stagedUpload{Filename: filepath.Base(req.Filename), Size: req.Size, Created: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		_ = os.RemoveAll(dir)
		a.apiServerError(w, r, "stage upload", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644); err != nil {
		_ = os.RemoveAll(dir)
		a.apiServerError(w, r, "stage upload", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "chunk_size": uploadChunkSize})
}

func (a *application) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	meta, dir, err := a.readStaging(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, os.ErrInvalid) || errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown upload"})
			return
		}
		a.apiServerError(w, r, "read staged upload", err)
		return
	}
	var received int64
	if info, err := os.Stat(filepath.Join(dir, "data.bin")); err == nil {
		received = info.Size()
	} else if !os.IsNotExist(err) {
		a.apiServerError(w, r, "read staged upload", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "filename": meta.Filename, "size": meta.Size, "received": received, "chunk_size": uploadChunkSize})
}

func (a *application) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	meta, dir, err := a.readStaging(id)
	if err != nil {
		if errors.Is(err, os.ErrInvalid) || errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown upload"})
			return
		}
		a.apiServerError(w, r, "read staged upload", err)
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "offset required"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, uploadChunkSize+(1<<20))
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Duration(a.cfg.UploadTimeoutSeconds) * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		a.apiServerError(w, r, "set upload read deadline", err)
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		var timeout net.Error
		switch {
		case errors.As(err, &tooLarge):
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "chunk exceeds chunk size"})
		case errors.As(err, &timeout) && timeout.Timeout():
			writeJSON(w, http.StatusRequestTimeout, map[string]string{"error": "upload timed out"})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid chunk"})
		}
		return
	}
	a.claimMu.Lock()
	defer a.claimMu.Unlock()
	var received int64
	if info, err := os.Stat(filepath.Join(dir, "data.bin")); err == nil {
		received = info.Size()
	} else if !os.IsNotExist(err) {
		a.apiServerError(w, r, "read staged upload", err)
		return
	}
	if offset != received {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "offset mismatch", "received": received})
		return
	}
	if received+int64(len(data)) > meta.Size {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "upload exceeds declared size"})
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "data.bin"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		a.apiServerError(w, r, "stage chunk", err)
		return
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		a.apiServerError(w, r, "stage chunk", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": received + int64(len(data))})
}

func (a *application) handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	meta, dir, err := a.readStaging(id)
	if err != nil {
		if errors.Is(err, os.ErrInvalid) || errors.Is(err, os.ErrNotExist) {
			writeAPIError(w, r, http.StatusNotFound, "error.unknownUpload")
			return
		}
		a.apiServerError(w, r, "read staged upload", err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxUploadBytes)
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Duration(a.cfg.UploadTimeoutSeconds) * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		a.apiServerError(w, r, "set upload read deadline", err)
		return
	}
	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "error.multipart")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				log.Printf("multipart cleanup: %v", err)
			}
		}
	}()
	title, desc, category := strings.TrimSpace(r.FormValue("title")), r.FormValue("desc"), r.FormValue("category")
	if title == "" || len(title) > 512 || len(desc) > 8192 || len(category) > 256 {
		writeAPIError(w, r, http.StatusBadRequest, "error.invalidFields")
		return
	}
	accessList, ok := a.resolveAccessList(w, r)
	if !ok {
		return
	}
	staged := filepath.Join(dir, "data.bin")
	info, err := os.Stat(staged)
	if err != nil || info.Size() != meta.Size {
		writeAPIError(w, r, http.StatusBadRequest, "error.uploadIncomplete")
		return
	}
	ext := strings.ToLower(filepath.Ext(meta.Filename))
	f, err := os.Open(staged)
	if err != nil {
		a.apiServerError(w, r, "read staged upload", err)
		return
	}
	valid := hasMediaHeader(f, ext)
	f.Close()
	if !valid {
		writeAPIError(w, r, http.StatusUnsupportedMediaType, "error.mediaHeader")
		return
	}
	orig := "original" + ext
	a.finishUpload(w, r, title, desc, category, accessList, orig, func(videoDir string) error {
		if err := os.Rename(staged, filepath.Join(videoDir, orig)); err != nil {
			return err
		}
		return os.RemoveAll(dir)
	})
}

// finishUpload publishes a materialized original through the same pipeline
// as single-shot uploads: exclusive directory, metadata, workers, audit and
// the browser/JSON response. The stage callback moves the validated original
// into the reserved directory.
func (a *application) finishUpload(w http.ResponseWriter, r *http.Request, title, desc, category, accessList, orig string, stage func(videoDir string) error) {
	id, dir, err := reserveUploadDir(a.videoDir)
	if err != nil {
		a.apiServerError(w, r, "reserve upload", err)
		return
	}
	keep := false
	defer func() {
		// dir is exclusively created by reserveUploadDir under our configured root.
		if !keep {
			if err := os.RemoveAll(dir); err != nil {
				log.Printf("incomplete upload cleanup %s: %v", id, err)
			}
		}
	}()
	if err := stage(dir); err != nil {
		a.apiServerError(w, r, "save original", err)
		return
	}
	m := store.Meta{ID: id, Title: title, Desc: desc, Category: category, Original: orig, Status: "ready", Created: time.Now().UTC().Format(time.RFC3339Nano), AccessList: accessList}
	if a.ffmpegBin != "" {
		m.Status = "queued"
	}
	// Keep list validation and metadata publication atomic with list deletion.
	a.claimMu.Lock()
	if accessList != "" {
		if _, ok := a.resolveAccessList(w, r); !ok {
			a.claimMu.Unlock()
			return
		}
	}
	err = a.putMeta(m)
	a.claimMu.Unlock()
	if err != nil {
		a.apiServerError(w, r, "save metadata", err)
		return
	}
	keep = true
	if m.Status == "queued" {
		a.notifyWorkers()
	}
	a.audit(r, auditVideoUpload, id, title)
	if wantsJSON(r) {
		writeJSON(w, http.StatusCreated, map[string]any{
			"id": id, "title": title, "desc": desc, "category": category,
			"status": m.Status, "original": orig, "access_list": accessList,
			"watch_url": "/watch?id=" + id,
			"video_url": "/videos/" + id + "/" + orig,
			"api_url":   "/api/video?id=" + id,
		})
		return
	}
	http.Redirect(w, r, "/watch?id="+id, http.StatusSeeOther)
}

// pruneStaleUploads removes interrupted chunked-upload staging directories
// older than maxStagingAge. Active uploads keep their directory current with
// every chunk.
func pruneStaleUploads(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".staging-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > maxStagingAge {
			if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
				log.Printf("stale upload cleanup %s: %v", e.Name(), err)
			} else {
				log.Printf("removed stale upload %s", e.Name())
			}
		}
	}
}
