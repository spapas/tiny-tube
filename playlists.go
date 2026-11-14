package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"tiny-tube/store"
)

// Playlists are operator-curated ordered series. Creation, renaming, deletion
// and item edits share the admin surface; playback order is fixed while each
// video keeps its own visibility. Item mutations share the metadata mutation
// lock with locked-list removal.

func (a *application) parsePlaylistForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "error.invalidForm")
		return false
	}
	return true
}

func (a *application) handleAdminPlaylists(w http.ResponseWriter, r *http.Request) {
	data, err := a.adminData(r, "playlists")
	if err != nil {
		a.serverError(w, "admin data", err)
		return
	}
	lists, err := a.fs.ListPlaylists()
	if err != nil {
		a.serverError(w, "list playlists", err)
		return
	}
	data["Playlists"] = lists
	a.renderPage(w, r, adminPlaylistsTmpl, data)
}

func (a *application) handleAdminPlaylistEdit(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	p, err := a.fs.GetPlaylist(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrInvalid) {
			adminRedirectError(w, r, "/admin/playlists", "error.playlistNotFound")
		} else {
			a.serverError(w, "read playlist", err)
		}
		return
	}
	data, err := a.adminData(r, "playlists")
	if err != nil {
		a.serverError(w, "admin data", err)
		return
	}
	type playlistItem struct {
		Meta    store.Meta
		Missing bool
	}
	items := []playlistItem{}
	for _, videoID := range p.Items {
		m, err := a.fs.Get(videoID)
		if err != nil {
			items = append(items, playlistItem{Meta: store.Meta{ID: videoID}, Missing: true})
			continue
		}
		items = append(items, playlistItem{Meta: m})
	}
	data["Playlist"] = p
	data["PlaylistItems"] = items
	a.renderPage(w, r, adminPlaylistEditTmpl, data)
}

func (a *application) handleAdminCreatePlaylist(w http.ResponseWriter, r *http.Request) {
	if !a.parsePlaylistForm(w, r) {
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if !store.ValidPlaylistName(name) {
		adminRedirectError(w, r, "/admin/playlists", "error.invalidFields")
		return
	}
	id, err := store.GeneratePlaylistID()
	if err != nil {
		a.apiServerError(w, r, "generate playlist id", err)
		return
	}
	if err := a.fs.PutPlaylist(store.Playlist{ID: id, Name: name}); err != nil {
		a.apiServerError(w, r, "create playlist", err)
		return
	}
	log.Printf("admin %q created playlist %q (%s)", currentUser(r).Username, name, id)
	a.audit(r, auditPlaylistCreate, id, name)
	http.Redirect(w, r, "/admin/playlists/edit?id="+id, http.StatusSeeOther)
}

func (a *application) handleAdminRenamePlaylist(w http.ResponseWriter, r *http.Request) {
	if !a.parsePlaylistForm(w, r) {
		return
	}
	id := strings.TrimSpace(r.PostForm.Get("id"))
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if !store.ValidPlaylistID(id) || !store.ValidPlaylistName(name) {
		adminRedirectError(w, r, "/admin/playlists", "error.invalidFields")
		return
	}
	a.claimMu.Lock()
	defer a.claimMu.Unlock()
	p, err := a.fs.GetPlaylist(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			adminRedirectError(w, r, "/admin/playlists", "error.playlistNotFound")
		} else {
			a.apiServerError(w, r, "read playlist", err)
		}
		return
	}
	p.Name = name
	if err := a.fs.PutPlaylist(p); err != nil {
		a.apiServerError(w, r, "rename playlist", err)
		return
	}
	log.Printf("admin %q renamed playlist %q to %q", currentUser(r).Username, id, name)
	a.audit(r, auditPlaylistRename, id, name)
	adminRedirectNotice(w, r, "/admin/playlists/edit?id="+id, "notice.playlistUpdated")
}

func (a *application) handleAdminDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	if !a.parsePlaylistForm(w, r) {
		return
	}
	id := strings.TrimSpace(r.PostForm.Get("id"))
	if !store.ValidPlaylistID(id) {
		adminRedirectError(w, r, "/admin/playlists", "error.playlistNotFound")
		return
	}
	a.claimMu.Lock()
	defer a.claimMu.Unlock()
	if err := a.fs.DeletePlaylist(id); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			adminRedirectError(w, r, "/admin/playlists", "error.playlistNotFound")
		} else {
			a.apiServerError(w, r, "delete playlist", err)
		}
		return
	}
	log.Printf("admin %q deleted playlist %q", currentUser(r).Username, id)
	a.audit(r, auditPlaylistDelete, id, "")
	adminRedirectNotice(w, r, "/admin/playlists", "notice.playlistUpdated")
}

func (a *application) handlePlaylistAddItem(w http.ResponseWriter, r *http.Request) {
	if !a.parsePlaylistForm(w, r) {
		return
	}
	id := strings.TrimSpace(r.PostForm.Get("id"))
	videoID := strings.TrimSpace(r.PostForm.Get("video_id"))
	if !store.ValidPlaylistID(id) {
		adminRedirectError(w, r, "/admin/playlists", "error.playlistNotFound")
		return
	}
	if !store.ValidID(videoID) {
		adminRedirectError(w, r, "/admin/playlists/edit?id="+id, "error.videoNotFound")
		return
	}
	a.claimMu.Lock()
	defer a.claimMu.Unlock()
	p, err := a.fs.GetPlaylist(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			adminRedirectError(w, r, "/admin/playlists", "error.playlistNotFound")
		} else {
			a.apiServerError(w, r, "read playlist", err)
		}
		return
	}
	if _, err := a.fs.Get(videoID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			adminRedirectError(w, r, "/admin/playlists/edit?id="+id, "error.videoNotFound")
		} else {
			a.apiServerError(w, r, "read video", err)
		}
		return
	}
	for _, item := range p.Items {
		if item == videoID {
			adminRedirectError(w, r, "/admin/playlists/edit?id="+id, "error.playlistDuplicate")
			return
		}
	}
	if len(p.Items) >= store.MaxPlaylistItems {
		adminRedirectError(w, r, "/admin/playlists/edit?id="+id, "error.playlistFull")
		return
	}
	p.Items = append(p.Items, videoID)
	if err := a.fs.PutPlaylist(p); err != nil {
		a.apiServerError(w, r, "add playlist item", err)
		return
	}
	log.Printf("admin %q added video %q to playlist %q", currentUser(r).Username, videoID, id)
	a.audit(r, auditPlaylistItems, id, "add:"+videoID)
	adminRedirectNotice(w, r, "/admin/playlists/edit?id="+id, "notice.playlistUpdated")
}

func (a *application) handlePlaylistRemoveItem(w http.ResponseWriter, r *http.Request) {
	if !a.parsePlaylistForm(w, r) {
		return
	}
	id := strings.TrimSpace(r.PostForm.Get("id"))
	videoID := strings.TrimSpace(r.PostForm.Get("video_id"))
	if !store.ValidPlaylistID(id) || !store.ValidID(videoID) {
		adminRedirectError(w, r, "/admin/playlists", "error.playlistNotFound")
		return
	}
	a.claimMu.Lock()
	defer a.claimMu.Unlock()
	p, err := a.fs.GetPlaylist(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			adminRedirectError(w, r, "/admin/playlists", "error.playlistNotFound")
		} else {
			a.apiServerError(w, r, "read playlist", err)
		}
		return
	}
	kept := p.Items[:0]
	for _, item := range p.Items {
		if item != videoID {
			kept = append(kept, item)
		}
	}
	p.Items = kept
	if err := a.fs.PutPlaylist(p); err != nil {
		a.apiServerError(w, r, "remove playlist item", err)
		return
	}
	log.Printf("admin %q removed video %q from playlist %q", currentUser(r).Username, videoID, id)
	a.audit(r, auditPlaylistItems, id, "remove:"+videoID)
	adminRedirectNotice(w, r, "/admin/playlists/edit?id="+id, "notice.playlistUpdated")
}

func (a *application) handlePlaylistMoveItem(w http.ResponseWriter, r *http.Request) {
	if !a.parsePlaylistForm(w, r) {
		return
	}
	id := strings.TrimSpace(r.PostForm.Get("id"))
	videoID := strings.TrimSpace(r.PostForm.Get("video_id"))
	direction := strings.TrimSpace(r.PostForm.Get("direction"))
	if !store.ValidPlaylistID(id) || !store.ValidID(videoID) || (direction != "up" && direction != "down") {
		adminRedirectError(w, r, "/admin/playlists", "error.playlistNotFound")
		return
	}
	a.claimMu.Lock()
	defer a.claimMu.Unlock()
	p, err := a.fs.GetPlaylist(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			adminRedirectError(w, r, "/admin/playlists", "error.playlistNotFound")
		} else {
			a.apiServerError(w, r, "read playlist", err)
		}
		return
	}
	at := -1
	for i, item := range p.Items {
		if item == videoID {
			at = i
			break
		}
	}
	if at >= 0 {
		other := at - 1
		if direction == "down" {
			other = at + 1
		}
		if other >= 0 && other < len(p.Items) {
			p.Items[at], p.Items[other] = p.Items[other], p.Items[at]
			if err := a.fs.PutPlaylist(p); err != nil {
				a.apiServerError(w, r, "move playlist item", err)
				return
			}
			a.audit(r, auditPlaylistItems, id, "move-"+direction+":"+videoID)
		}
	}
	adminRedirectNotice(w, r, "/admin/playlists/edit?id="+id, "notice.playlistUpdated")
}

// handlePlaylist serves one series in fixed order: /playlist?id=... Videos
// keep their own visibility, so locked videos stay hidden without a key and
// missing videos are skipped instead of breaking the page.
func (a *application) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	p, err := a.fs.GetPlaylist(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	grants := a.resolveGrants(w, r)
	var all []store.Meta
	for _, videoID := range p.Items {
		m, err := a.fs.Get(videoID)
		if err != nil {
			continue
		}
		all = append(all, m)
	}
	items := all
	if u := currentUser(r); u == nil || !u.CanEdit() {
		items = a.visibleVideos(all, r, grants)
	}
	data := map[string]any{
		"Page": "playlist", "Title": p.Name, "ListName": p.Name,
		"Items": items, "PlaylistID": p.ID,
	}
	a.renderPage(w, r, playlistTmpl, data)
}

// playlistNeighbors locates the visible videos around id in playlistID for
// the watch page series pager. Absent playlists, videos outside the series
// and hidden neighbors yield no links.
func (a *application) playlistNeighbors(w http.ResponseWriter, r *http.Request, playlistID, id string) (prev, next *store.Meta) {
	p, err := a.fs.GetPlaylist(playlistID)
	if err != nil {
		return nil, nil
	}
	at := -1
	for i, item := range p.Items {
		if item == id {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, nil
	}
	grants := a.resolveGrants(w, r)
	u := currentUser(r)
	visible := func(videoID string) *store.Meta {
		m, err := a.fs.Get(videoID)
		if err != nil {
			return nil
		}
		if u == nil || !u.CanEdit() {
			if len(a.visibleVideos([]store.Meta{m}, r, grants)) == 0 {
				return nil
			}
		}
		return &m
	}
	if at > 0 {
		prev = visible(p.Items[at-1])
	}
	if at+1 < len(p.Items) {
		next = visible(p.Items[at+1])
	}
	return prev, next
}
