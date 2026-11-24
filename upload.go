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
		writeAPIError(w, r, http.StatusRequestEntityTooLarge, "error.uploadTooLarge")
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
			writeAPIError(w, r, http.StatusRequestEntityTooLarge, "error.uploadTooLarge")
		} else if errors.As(err, &timeout) && timeout.Timeout() {
			writeAPIError(w, r, http.StatusRequestTimeout, "error.uploadTimeout")
		} else {
			writeAPIError(w, r, http.StatusBadRequest, "error.multipart")
		}
		return
	}
	title, desc, category := strings.TrimSpace(r.FormValue("title")), r.FormValue("desc"), r.FormValue("category")
	if title == "" || len(title) > 512 || len(desc) > 8192 || len(category) > 256 {
		writeAPIError(w, r, http.StatusBadRequest, "error.invalidFields")
		return
	}
	accessList, ok := a.resolveAccessList(w, r)
	if !ok {
		return
	}
	if a.cfg.QuotaBytes > 0 {
		var extra int64
		if r.ContentLength > 0 {
			extra = r.ContentLength
		}
		exceeded, err := a.quotaExceeded(extra)
		if err != nil {
			a.apiServerError(w, r, "measure storage usage", err)
			return
		}
		if exceeded {
			writeAPIError(w, r, http.StatusRequestEntityTooLarge, "error.quotaExceeded")
			return
		}
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "error.fileRequired")
		return
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if ext != ".mp4" && ext != ".mov" && ext != ".mkv" && ext != ".webm" {
		writeAPIError(w, r, http.StatusUnsupportedMediaType, "error.formats")
		return
	}
	if !hasMediaHeader(f, ext) {
		writeAPIError(w, r, http.StatusUnsupportedMediaType, "error.mediaHeader")
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		a.apiServerError(w, r, "rewind upload", err)
		return
	}
	orig := "original" + ext
	a.finishUpload(w, r, title, desc, category, accessList, orig, func(dir string) error {
		dst, err := os.OpenFile(filepath.Join(dir, orig), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(dst, f)
		syncErr := dst.Sync()
		closeErr := dst.Close()
		if copyErr == nil {
			a.metrics.uploadBytes.add(float64(n))
		}
		return errors.Join(copyErr, syncErr, closeErr)
	})
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
