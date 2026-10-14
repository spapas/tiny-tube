package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tiny-tube/store"
)

const multipartMemory = 8 << 20

func reserveUploadDir(root string) (string, string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", "", err
		}
		id := hex.EncodeToString(random[:])
		dir := filepath.Join(root, id)
		if err := os.Mkdir(dir, 0o755); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", "", err
		}
		return id, dir, nil
	}
	return "", "", fmt.Errorf("could not reserve unique upload directory")
}

func (a *application) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxUploadBytes)
	if r.ContentLength > a.cfg.MaxUploadBytes {
		http.Error(w, "upload exceeds MAX_UPLOAD_BYTES", http.StatusRequestEntityTooLarge)
		return
	}
	err := r.ParseMultipartForm(multipartMemory)
	defer func() {
		if r.MultipartForm != nil {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				log.Printf("multipart cleanup: %v", err)
			}
		}
	}()
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "upload exceeds MAX_UPLOAD_BYTES", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid multipart upload", http.StatusBadRequest)
		}
		return
	}
	title, desc, category := strings.TrimSpace(r.FormValue("title")), r.FormValue("desc"), r.FormValue("category")
	if title == "" || len(title) > 512 || len(desc) > 8192 || len(category) > 256 {
		http.Error(w, "title required; maximum bytes: title 512, description 8192, category 256", http.StatusBadRequest)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if ext != ".mp4" && ext != ".mov" && ext != ".mkv" && ext != ".webm" {
		http.Error(w, "supported formats: mp4, mov, mkv, webm", http.StatusUnsupportedMediaType)
		return
	}
	if !hasMediaHeader(f, ext) {
		http.Error(w, "file does not have a supported media header", http.StatusUnsupportedMediaType)
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		a.serverError(w, "rewind upload", err)
		return
	}
	id, dir, err := reserveUploadDir(a.videoDir)
	if err != nil {
		a.serverError(w, "reserve upload", err)
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
	orig := "original" + ext
	dst, err := os.OpenFile(filepath.Join(dir, orig), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		a.serverError(w, "create original", err)
		return
	}
	_, copyErr := io.Copy(dst, f)
	syncErr := dst.Sync()
	closeErr := dst.Close()
	if err = errors.Join(copyErr, syncErr, closeErr); err != nil {
		a.serverError(w, "save original", err)
		return
	}
	m := store.Meta{ID: id, Title: title, Desc: desc, Category: category, Original: orig, Status: "ready", Created: time.Now().UTC().Format(time.RFC3339Nano)}
	if a.ffmpegBin != "" {
		m.Status = "queued"
	}
	if err = a.fs.Put(m); err != nil {
		a.serverError(w, "save metadata", err)
		return
	}
	keep = true
	if m.Status == "queued" {
		a.notifyWorkers()
	}
	http.Redirect(w, r, "/watch?id="+id, http.StatusSeeOther)
}

// This checks container signatures, not codec validity or the complete file.
func hasMediaHeader(r io.Reader, ext string) bool {
	var header [12]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return false
	}
	if ext == ".mkv" || ext == ".webm" {
		return header[0] == 0x1a && header[1] == 0x45 && header[2] == 0xdf && header[3] == 0xa3
	}
	atom := string(header[4:8])
	if ext == ".mp4" {
		return atom == "ftyp"
	}
	return atom == "ftyp" || atom == "moov" || atom == "mdat" || atom == "wide" || atom == "free"
}
