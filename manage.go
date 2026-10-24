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
	a.renderPage(w, r, editTmpl, map[string]any{"Page": "edit", "Title": "Επεξεργασία βίντεο", "Video": m, "AccessLists": lists})
}

func parseManagementForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, r, http.StatusRequestEntityTooLarge, "form too large")
		} else {
			writeAPIError(w, r, http.StatusBadRequest, "invalid form")
		}
		return false
	}
	if r.PostForm.Get("id") == "" {
		writeAPIError(w, r, http.StatusBadRequest, "id required")
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
		writeAPIError(w, r, http.StatusConflict, "video deletion in progress")
		return
	}
	m, ok := a.readVideo(w, r, id)
	if !ok {
		return
	}
	if title == "" || len(title) > 512 || len(desc) > 8192 || len(category) > 256 {
		if wantsJSON(r) {
			writeAPIError(w, r, http.StatusBadRequest, "title required; maximum bytes: title 512, description 8192, category 256")
		} else {
			lists, ok := a.accessListsForForm(w, r)
			if !ok {
				return
			}
			m.Title, m.Desc, m.Category = title, desc, category
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			a.renderPage(w, r, editTmpl, map[string]any{"Page": "edit", "Title": "Επεξεργασία βίντεο", "Video": m, "AccessLists": lists, "Error": "Συμπληρώστε τίτλο και συντομεύστε τα μεγάλα πεδία. Οι ελληνικοί χαρακτήρες μπορεί να καταλαμβάνουν περισσότερα bytes."})
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

func (a *application) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !parseManagementForm(w, r) {
		return
	}
	id := r.PostForm.Get("id")
	a.claimMu.Lock()
	if a.deleting[id] {
		a.claimMu.Unlock()
		writeAPIError(w, r, http.StatusConflict, "video deletion in progress")
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
			writeAPIError(w, r, http.StatusServiceUnavailable, "conversion did not stop; retry deletion")
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
