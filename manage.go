package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"tiny-tube/store"
)

func (a *application) readVideo(w http.ResponseWriter, r *http.Request, id string) (store.Meta, bool) {
	m, err := a.fs.Get(id)
	if err == nil {
		return m, true
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrInvalid) {
		if wantsJSON(r) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		} else {
			http.NotFound(w, r)
		}
	} else {
		a.apiServerError(w, r, "read video", err)
	}
	return m, false
}

func (a *application) handleEditPage(w http.ResponseWriter, r *http.Request) {
	m, ok := a.readVideo(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	lists, ok := a.accessListsForForm(w, r)
	if !ok {
		return
	}
	if m.AccessList != "" {
		found := false
		for _, list := range lists {
			if list.ID == m.AccessList {
				found = true
				break
			}
		}
		if !found {
			a.apiServerError(w, r, "read video visibility", errors.New("current access list unavailable"))
			return
		}
	}
	a.renderEditPage(w, r, m, lists, "")
}

func (a *application) renderEditPage(w http.ResponseWriter, r *http.Request, m store.Meta, lists []store.AccessList, formError string) {
	tracks, err := a.subtitleTracks(m.ID)
	if err != nil {
		a.apiServerError(w, r, "read subtitles", err)
		return
	}
	data := map[string]any{"Page": "edit", "Title": "edit.title", "Video": m, "AccessLists": lists, "Subtitles": tracks}
	if formError != "" {
		data["Error"] = formError
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
	}
	a.renderPage(w, r, editTmpl, data)
}

func parseManagementForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, r, http.StatusRequestEntityTooLarge, "error.formTooLarge")
		} else {
			writeAPIError(w, r, http.StatusBadRequest, "error.invalidForm")
		}
		return false
	}
	if r.PostForm.Get("id") == "" {
		writeAPIError(w, r, http.StatusBadRequest, "error.idRequired")
		return false
	}
	return true
}

func (a *application) handleEdit(w http.ResponseWriter, r *http.Request) {
	if !parseManagementForm(w, r) {
		return
	}
	title, desc, category := strings.TrimSpace(r.PostForm.Get("title")), strings.TrimSpace(r.PostForm.Get("desc")), strings.TrimSpace(r.PostForm.Get("category"))
	a.claimMu.Lock()
	defer a.claimMu.Unlock()
	id := r.PostForm.Get("id")
	if a.deleting[id] {
		writeAPIError(w, r, http.StatusConflict, "error.deletionInProgress")
		return
	}
	m, ok := a.readVideo(w, r, id)
	if !ok {
		return
	}
	if title == "" || len(title) > 512 || len(desc) > 8192 || len(category) > 256 {
		if wantsJSON(r) {
			writeAPIError(w, r, http.StatusBadRequest, "error.invalidFields")
		} else {
			lists, ok := a.accessListsForForm(w, r)
			if !ok {
				return
			}
			m.Title, m.Desc, m.Category = title, desc, category
			a.renderEditPage(w, r, m, lists, "edit.invalidFields")
		}
		return
	}
	// Omitted visibility preserves the existing policy for older API clients.
	accessList := m.AccessList
	if _, present := r.PostForm["access_list"]; present {
		var ok bool
		accessList, ok = a.resolveAccessList(w, r)
		if !ok {
			return
		}
	}
	m.Title, m.Desc, m.Category = title, desc, category
	m.AccessList = accessList
	if err := a.putMeta(m); err != nil {
		a.apiServerError(w, r, "edit video", err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"id": m.ID, "title": m.Title, "watch_url": "/watch?id=" + m.ID, "api_url": "/api/video?id=" + m.ID})
		return
	}
	http.Redirect(w, r, "/watch?id="+m.ID, http.StatusSeeOther)
}

func canQueueTranscode(m store.Meta) bool {
	return m.HLS == "" && (m.Status == "failed" || m.Status == "ready")
}

func (a *application) handleRetryTranscode(w http.ResponseWriter, r *http.Request) {
	if !parseManagementForm(w, r) {
		return
	}
	id := r.PostForm.Get("id")
	a.claimMu.Lock()
	defer a.claimMu.Unlock()
	if a.deleting[id] {
		writeAPIError(w, r, http.StatusConflict, "error.deletionInProgress")
		return
	}
	m, ok := a.readVideo(w, r, id)
	if !ok {
		return
	}
	if a.ffmpegBin == "" {
		writeAPIError(w, r, http.StatusServiceUnavailable, "error.transcodeUnavailable")
		return
	}
	if !canQueueTranscode(m) {
		writeAPIError(w, r, http.StatusConflict, "error.transcodeState")
		return
	}
	if _, err := originalFormat(m.Original); err != nil {
		writeAPIError(w, r, http.StatusConflict, "error.transcodeOriginal")
		return
	}
	info, err := a.mediaRoot.Stat(id + "/" + m.Original)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		writeAPIError(w, r, http.StatusConflict, "error.transcodeOriginal")
		return
	}
	if err != nil {
		a.apiServerError(w, r, "check original for conversion", err)
		return
	}
	m.Status, m.Error = "queued", ""
	if err := a.putMeta(m); err != nil {
		a.apiServerError(w, r, "queue video conversion", err)
		return
	}
	a.notifyWorkers()
	if wantsJSON(r) {
		writeJSON(w, http.StatusAccepted, map[string]string{"id": id, "status": m.Status, "watch_url": "/watch?id=" + id})
		return
	}
	http.Redirect(w, r, "/watch?id="+id, http.StatusSeeOther)
}

func (a *application) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !parseManagementForm(w, r) {
		return
	}
	id := r.PostForm.Get("id")
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
	a.deleting[id] = true
	job := a.activeJobs[id]
	if job != nil {
		job.cancel()
	}
	a.claimMu.Unlock()
	deleted := false
	defer func() {
		a.claimMu.Lock()
		delete(a.deleting, id)
		// If deletion failed after cancellation, allow the interrupted job to resume.
		if !deleted && a.activeJobs[id] == nil {
			if m, err := a.fs.Get(id); err == nil && m.Status == "processing" {
				m.Status = "queued"
				if err := a.putMeta(m); err != nil {
					log.Printf("restore canceled job %s: %v", id, err)
				}
			}
		}
		a.claimMu.Unlock()
		if !deleted {
			a.notifyWorkers()
		}
	}()
	if job != nil {
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-job.done:
		case <-r.Context().Done():
			return
		case <-timer.C:
			writeAPIError(w, r, http.StatusServiceUnavailable, "error.conversionNotStopped")
			return
		}
	}
	a.claimMu.Lock()
	err := a.deleteMeta(id)
	a.claimMu.Unlock()
	if err != nil {
		a.apiServerError(w, r, "delete video", err)
		return
	}
	deleted = true
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
