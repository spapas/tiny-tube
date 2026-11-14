package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

const (
	subtitleDir         = "subtitles"
	subtitleFilePrefix  = "track-"
	maxSubtitleBytes    = 1 << 20
	maxSubtitleRequest  = maxSubtitleBytes + (16 << 10)
	maxSubtitleLanguage = 63
)

var subtitleLanguage = regexp.MustCompile(`^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$`)

type subtitleTrack struct {
	Language string
	Label    string
	File     string
}

func parseSubtitleLanguage(value string) (subtitleTrack, bool) {
	if len(value) > maxSubtitleLanguage || !subtitleLanguage.MatchString(value) {
		return subtitleTrack{}, false
	}
	tag, err := language.Parse(value)
	if err != nil || len(tag.String()) > maxSubtitleLanguage {
		return subtitleTrack{}, false
	}
	code := tag.String()
	return subtitleTrack{Language: code, File: subtitleFilePrefix + strings.ToLower(code) + ".vtt"}, true
}

func subtitleTrackFromFile(name string) (subtitleTrack, bool) {
	code, hasPrefix := strings.CutPrefix(name, subtitleFilePrefix)
	code, hasSuffix := strings.CutSuffix(code, ".vtt")
	if !hasPrefix || !hasSuffix {
		return subtitleTrack{}, false
	}
	track, ok := parseSubtitleLanguage(code)
	return track, ok && name == track.File
}

func (a *application) subtitleTracks(id string) ([]subtitleTrack, error) {
	video, err := a.mediaRoot.OpenRoot(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer video.Close()
	entries, err := fs.ReadDir(video.FS(), subtitleDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tracks []subtitleTrack
	for _, entry := range entries {
		track, ok := subtitleTrackFromFile(entry.Name())
		if !ok || !entry.Type().IsRegular() {
			continue
		}
		track.Label = display.Self.Name(language.Make(track.Language))
		if track.Label == "" {
			track.Label = track.Language
		}
		tracks = append(tracks, track)
	}
	return tracks, nil
}

func validWebVTT(data []byte) bool {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	end := bytes.IndexAny(data, "\r\n")
	if end < 0 {
		return false
	}
	header, ok := bytes.CutPrefix(data[:end], []byte("WEBVTT"))
	if !ok || len(header) != 0 && header[0] != ' ' && header[0] != '\t' {
		return false
	}
	if data[end] == '\r' && end+1 < len(data) && data[end+1] == '\n' {
		end++
	}
	return end+1 < len(data) && (data[end+1] == '\r' || data[end+1] == '\n')
}

func (a *application) handleSubtitleUpload(w http.ResponseWriter, r *http.Request) {
	limit := min(int64(maxSubtitleRequest), a.cfg.MaxUploadBytes)
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	err := r.ParseMultipartForm(maxSubtitleRequest)
	if r.MultipartForm != nil {
		defer func() {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				log.Printf("subtitle multipart cleanup: %v", err)
			}
		}()
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		var timeout net.Error
		switch {
		case errors.As(err, &tooLarge):
			writeAPIError(w, r, http.StatusRequestEntityTooLarge, "error.subtitleTooLarge")
		case errors.As(err, &timeout) && timeout.Timeout():
			writeAPIError(w, r, http.StatusRequestTimeout, "error.uploadTimeout")
		default:
			writeAPIError(w, r, http.StatusBadRequest, "error.multipart")
		}
		return
	}
	track, ok := parseSubtitleLanguage(strings.TrimSpace(r.PostForm.Get("language")))
	if !ok {
		writeAPIError(w, r, http.StatusBadRequest, "error.subtitleLanguage")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "error.fileRequired")
		return
	}
	defer file.Close()
	if header.Size > maxSubtitleBytes {
		writeAPIError(w, r, http.StatusRequestEntityTooLarge, "error.subtitleTooLarge")
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		a.apiServerError(w, r, "read subtitle upload", err)
		return
	}
	if !strings.EqualFold(filepath.Ext(header.Filename), ".vtt") || !validWebVTT(data) {
		writeAPIError(w, r, http.StatusUnsupportedMediaType, "error.subtitleFormat")
		return
	}
	a.changeSubtitle(w, r, track, data)
}

func (a *application) handleSubtitleDelete(w http.ResponseWriter, r *http.Request) {
	if !parseManagementForm(w, r) {
		return
	}
	track, ok := parseSubtitleLanguage(strings.TrimSpace(r.PostForm.Get("language")))
	if !ok {
		writeAPIError(w, r, http.StatusBadRequest, "error.subtitleLanguage")
		return
	}
	a.changeSubtitle(w, r, track, nil)
}

func (a *application) changeSubtitle(w http.ResponseWriter, r *http.Request, track subtitleTrack, data []byte) {
	id := r.PostForm.Get("id")
	if id == "" {
		writeAPIError(w, r, http.StatusBadRequest, "error.idRequired")
		return
	}
	a.claimMu.Lock()
	if a.deleting[id] {
		a.claimMu.Unlock()
		writeAPIError(w, r, http.StatusConflict, "error.deletionInProgress")
		return
	}
	if _, ok := a.readVideo(w, r, id); !ok {
		a.claimMu.Unlock()
		return
	}
	err := a.updateSubtitleFile(id, track.File, data)
	a.claimMu.Unlock()
	if err != nil {
		if data == nil && errors.Is(err, os.ErrNotExist) {
			writeAPIError(w, r, http.StatusNotFound, "error.subtitleNotFound")
		} else {
			a.apiServerError(w, r, "update subtitle file", err)
		}
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]string{"id": id, "language": track.Language})
		return
	}
	http.Redirect(w, r, "/edit?id="+id+"#subtitles", http.StatusSeeOther)
}

func (a *application) updateSubtitleFile(id, name string, data []byte) error {
	video, err := a.mediaRoot.OpenRoot(id)
	if err != nil {
		return err
	}
	defer video.Close()
	if data != nil {
		if err := video.Mkdir(subtitleDir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	dir, err := video.OpenRoot(subtitleDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	if data == nil {
		return dir.Remove(name)
	}
	// Publish a complete file in one rename and keep the old track on failure.
	tmp := ".upload-" + rand.Text()
	file, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer dir.Remove(tmp)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	syncErr := file.Sync()
	if err := errors.Join(syncErr, file.Close()); err != nil {
		return err
	}
	return publishSubtitle(video, dir, tmp, name)
}
