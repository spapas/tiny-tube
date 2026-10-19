package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
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
		writeAPIError(w, r, http.StatusRequestEntityTooLarge, "upload exceeds MAX_UPLOAD_BYTES")
		return
	}
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Duration(a.cfg.UploadTimeoutSeconds) * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		a.apiServerError(w, r, "set upload read deadline", err)
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
		var timeout net.Error
		if errors.As(err, &tooLarge) {
			writeAPIError(w, r, http.StatusRequestEntityTooLarge, "upload exceeds MAX_UPLOAD_BYTES")
		} else if errors.As(err, &timeout) && timeout.Timeout() {
			writeAPIError(w, r, http.StatusRequestTimeout, "upload timed out")
		} else {
			writeAPIError(w, r, http.StatusBadRequest, "invalid multipart upload")
		}
		return
	}
	title, desc, category := strings.TrimSpace(r.FormValue("title")), r.FormValue("desc"), r.FormValue("category")
	if title == "" || len(title) > 512 || len(desc) > 8192 || len(category) > 256 {
		writeAPIError(w, r, http.StatusBadRequest, "title required; maximum bytes: title 512, description 8192, category 256")
		return
	}
	accessList, ok := a.resolveAccessList(w, r)
	if !ok {
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "file required")
		return
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if ext != ".mp4" && ext != ".mov" && ext != ".mkv" && ext != ".webm" {
		writeAPIError(w, r, http.StatusUnsupportedMediaType, "supported formats: mp4, mov, mkv, webm")
		return
	}
	if !hasMediaHeader(f, ext) {
		writeAPIError(w, r, http.StatusUnsupportedMediaType, "file does not have a supported media header")
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		a.apiServerError(w, r, "rewind upload", err)
		return
	}
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
	orig := "original" + ext
	dst, err := os.OpenFile(filepath.Join(dir, orig), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		a.apiServerError(w, r, "create original", err)
		return
	}
	_, copyErr := io.Copy(dst, f)
	syncErr := dst.Sync()
	closeErr := dst.Close()
	if err = errors.Join(copyErr, syncErr, closeErr); err != nil {
		a.apiServerError(w, r, "save original", err)
		return
	}
	m := store.Meta{ID: id, Title: title, Desc: desc, Category: category, Original: orig, Status: "ready", Created: time.Now().UTC().Format(time.RFC3339Nano), AccessList: accessList}
	if a.ffmpegBin != "" {
		m.Status = "queued"
	}
	if err = a.putMeta(m); err != nil {
		a.apiServerError(w, r, "save metadata", err)
		return
	}
	keep = true
	if m.Status == "queued" {
		a.notifyWorkers()
	}
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
