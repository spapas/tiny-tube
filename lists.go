package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"

	"tiny-tube/store"
)

// Locked lists hide videos behind a shared long key. A video with an empty
// AccessList stays public. Otherwise it is invisible — list, search, counts,
// watch, JSON API and media files — unless the request carries the list key
// (?key=lt-...) or an unlock cookie granted by a previous key visit.
// Editors and administrators bypass the key so they can manage content.
//
// Keys are capability URLs: anyone holding the /view_list?key=... link sees
// the list. Rotation revokes every copy at once. Only the SHA-256 of a key
// is stored; the plaintext is shown once at creation/rotation.

const listGrantCookieName = "tiny_tube_lists"

// maxListGrants bounds the unlock cookie so it stays well under header limits.
const maxListGrants = 32

// findListByKey resolves a presented ?key= token to its list. Unknown or
// mismatched keys stay anonymous, like a stale session cookie.
func (a *application) findListByKey(raw string) (store.AccessList, bool) {
	var empty store.AccessList
	prefix, ok := store.SplitAccessKey(raw)
	if !ok {
		return empty, false
	}
	l, err := a.fs.GetAccessListByPrefix(prefix)
	if err != nil {
		return empty, false
	}
	if subtle.ConstantTimeCompare([]byte(l.KeyHash), []byte(store.HashAccessKey(raw))) != 1 {
		return empty, false
	}
	return l, true
}

// signListGrants authenticates the unlocked key prefixes so clients cannot
// forge access. The signing key is random per process; a restart drops
// existing grants and the next link visit restores them.
func (a *application) signListGrants(payload string) string {
	mac := hmac.New(sha256.New, a.listSignKey[:])
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// listGrantPrefixes returns the unlocked key prefixes from the request cookie.
// Prefixes (not list IDs) are stored so rotation and deletion revoke existing
// cookies: the old prefix simply resolves to nothing.
func (a *application) listGrantPrefixes(r *http.Request) []string {
	var prefixes []string
	cookie, err := r.Cookie(listGrantCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	payload, sig, ok := strings.Cut(cookie.Value, ".")
	if !ok || payload == "" || sig == "" {
		return nil
	}
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil
	}
	mac := hmac.New(sha256.New, a.listSignKey[:])
	mac.Write([]byte(payload))
	if subtle.ConstantTimeCompare(mac.Sum(nil), want) != 1 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || len(raw) > 4096 {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil
	}
	for _, id := range ids {
		if len(prefixes) >= maxListGrants {
			break
		}
		if store.ValidKeyPrefix(id) {
			prefixes = append(prefixes, id)
		}
	}
	return prefixes
}

// grantListPrefix adds a key prefix to the unlock cookie.
func (a *application) grantListPrefix(w http.ResponseWriter, r *http.Request, prefix string) {
	seen := map[string]bool{prefix: true}
	prefixes := []string{prefix}
	for _, p := range a.listGrantPrefixes(r) {
		if len(prefixes) >= maxListGrants {
			break
		}
		if !seen[p] {
			seen[p] = true
			prefixes = append(prefixes, p)
		}
	}
	sort.Strings(prefixes)
	payload, err := json.Marshal(prefixes)
	if err != nil {
		return
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	http.SetCookie(w, &http.Cookie{
		Name: listGrantCookieName, Value: encoded + "." + a.signListGrants(encoded),
		Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: a.cfg.CookieSecure,
	})
}

// resolveGrants merges cookie unlocks with a presented ?key=. A valid key
// grants its list for this request and refreshes the cookie so HLS segments,
// posters and later pages keep working without repeating the key. Prefixes
// are translated to list IDs per request, so rotated or deleted keys stop
// working even for existing cookies.
func (a *application) resolveGrants(w http.ResponseWriter, r *http.Request) map[string]bool {
	grants := map[string]bool{}
	prefixes := a.listGrantPrefixes(r)
	if raw := r.URL.Query().Get("key"); raw != "" {
		if l, ok := a.findListByKey(raw); ok {
			found := false
			for _, p := range prefixes {
				if p == l.Prefix {
					found = true
					break
				}
			}
			if !found {
				a.grantListPrefix(w, r, l.Prefix)
				prefixes = append(prefixes, l.Prefix)
			}
		}
	}
	for _, p := range prefixes {
		if l, err := a.fs.GetAccessListByPrefix(p); err == nil {
			grants[l.ID] = true
		}
	}
	return grants
}

// canViewList reports whether the request may see a video carrying the given
// access list. Editors and administrators bypass keys for management.
func canViewList(m store.Meta, u *store.User, grants map[string]bool) bool {
	if m.AccessList == "" {
		return true
	}
	if u != nil && u.CanEdit() {
		return true
	}
	return grants[m.AccessList]
}

func (a *application) canView(m store.Meta, r *http.Request, grants map[string]bool) bool {
	return canViewList(m, currentUser(r), grants)
}

// visibleVideos filters metadata to what the request may see. Locked videos
// vanish completely — including search matches and category counts.
func (a *application) visibleVideos(all []store.Meta, r *http.Request, grants map[string]bool) []store.Meta {
	u := currentUser(r)
	out := make([]store.Meta, 0, len(all))
	for _, m := range all {
		if canViewList(m, u, grants) {
			out = append(out, m)
		}
	}
	return out
}

// requestUserLazy resolves the session/API key only when locked content
// requires it, keeping public media serving free of session writes.
func (a *application) requestUserLazy(r *http.Request) *store.User {
	if u := a.validateSession(r); u != nil {
		return u
	}
	return a.validateAPIKey(r)
}

// handleViewList serves one locked list to key holders:
// /view_list?key=lt-... Without a key it shows everything the unlock cookie
// already opened. Unknown keys get 404 so existence stays unconfirmed.
func (a *application) handleViewList(w http.ResponseWriter, r *http.Request) {
	grants := a.resolveGrants(w, r)
	all, err := a.fs.List("")
	if err != nil {
		a.serverError(w, "list videos", err)
		return
	}
	if raw := r.URL.Query().Get("key"); raw != "" {
		l, ok := a.findListByKey(raw)
		if !ok {
			http.NotFound(w, r)
			return
		}
		items := make([]store.Meta, 0)
		for _, m := range all {
			if m.AccessList == l.ID {
				items = append(items, m)
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		a.renderPage(w, r, viewListTmpl, map[string]any{
			"Page": "view-list", "Title": l.Name, "ListName": l.Name,
			"Items": items, "Total": len(items), "ListKey": raw,
		})
		return
	}
	u := currentUser(r)
	items := make([]store.Meta, 0)
	for _, m := range all {
		if m.AccessList != "" && canViewList(m, u, grants) {
			items = append(items, m)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	a.renderPage(w, r, viewListTmpl, map[string]any{
		"Page": "view-list", "Title": "Κλειδωμένα βίντεο", "ListName": "",
		"Items": items, "Total": len(items), "ListKey": "",
	})
}

// resolveAccessList validates an upload/edit access_list field: empty means
// public, otherwise the list must exist.
func (a *application) resolveAccessList(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.FormValue("access_list"))
	if id == "" {
		return "", true
	}
	if !store.ValidAccessListID(id) {
		writeAPIError(w, r, http.StatusBadRequest, "unknown access list")
		return "", false
	}
	if _, err := a.fs.GetAccessList(id); err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrInvalid) {
			writeAPIError(w, r, http.StatusBadRequest, "unknown access list")
		} else {
			a.apiServerError(w, r, "read access list", err)
		}
		return "", false
	}
	return id, true
}

// accessListsForForm lists existing locked lists for the upload/edit picker.
func (a *application) accessListsForForm() []store.AccessList {
	lists, err := a.fs.ListAccessLists()
	if err != nil {
		log.Printf("list access lists: %v", err)
		return nil
	}
	return lists
}

// handleAdminLists shows the locked-list dashboard with per-list video counts.
func (a *application) handleAdminLists(w http.ResponseWriter, r *http.Request) {
	data, err := a.adminData(r, "lists")
	if err != nil {
		a.serverError(w, "admin data", err)
		return
	}
	lists, err := a.fs.ListAccessLists()
	if err != nil {
		a.serverError(w, "list access lists", err)
		return
	}
	counts := map[string]int{}
	if videos, err := a.fs.List(""); err == nil {
		for _, m := range videos {
			if m.AccessList != "" {
				counts[m.AccessList]++
			}
		}
	}
	data["AccessLists"] = lists
	data["ListVideoCounts"] = counts
	a.renderPage(w, r, adminListsTmpl, data)
}

func (a *application) renderAdminLists(w http.ResponseWriter, r *http.Request, extra map[string]any) {
	data, err := a.adminData(r, "lists")
	if err != nil {
		a.serverError(w, "admin data", err)
		return
	}
	lists, err := a.fs.ListAccessLists()
	if err != nil {
		a.serverError(w, "list access lists", err)
		return
	}
	counts := map[string]int{}
	if videos, err := a.fs.List(""); err == nil {
		for _, m := range videos {
			if m.AccessList != "" {
				counts[m.AccessList]++
			}
		}
	}
	data["AccessLists"] = lists
	data["ListVideoCounts"] = counts
	for k, v := range extra {
		data[k] = v
	}
	a.renderPage(w, r, adminListsTmpl, data)
}

func (a *application) parseListForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "invalid form")
		return false
	}
	return true
}

// handleAdminCreateList mints a list with its first key. Like API keys, the
// plaintext is rendered once, never redirected into URLs or logs.
func (a *application) handleAdminCreateList(w http.ResponseWriter, r *http.Request) {
	if !a.parseListForm(w, r) {
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if !store.ValidAccessListName(name) {
		adminRedirectError(w, r, "/admin/lists", "list name required (up to 100 characters)")
		return
	}
	id, err := store.GenerateAccessListID()
	if err != nil {
		a.apiServerError(w, r, "generate list id", err)
		return
	}
	raw, prefix, hash, err := store.GenerateAccessKey()
	if err != nil {
		a.apiServerError(w, r, "generate list key", err)
		return
	}
	if err := a.fs.PutAccessList(store.AccessList{ID: id, Name: name, Prefix: prefix, KeyHash: hash}); err != nil {
		a.apiServerError(w, r, "create access list", err)
		return
	}
	log.Printf("admin %q created access list %q (%s)", currentUser(r).Username, name, id)
	a.renderAdminLists(w, r, map[string]any{"NewListKey": raw, "NewListName": name, "NewListID": id})
}

func (a *application) handleAdminRenameList(w http.ResponseWriter, r *http.Request) {
	if !a.parseListForm(w, r) {
		return
	}
	id := strings.TrimSpace(r.PostForm.Get("id"))
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if !store.ValidAccessListID(id) || !store.ValidAccessListName(name) {
		adminRedirectError(w, r, "/admin/lists", "invalid list or name")
		return
	}
	l, err := a.fs.GetAccessList(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrInvalid) {
			adminRedirectError(w, r, "/admin/lists", "list not found")
		} else {
			a.apiServerError(w, r, "read access list", err)
		}
		return
	}
	l.Name = name
	if err := a.fs.PutAccessList(l); err != nil {
		a.apiServerError(w, r, "rename access list", err)
		return
	}
	log.Printf("admin %q renamed access list %q to %q", currentUser(r).Username, id, name)
	adminRedirectNotice(w, r, "/admin/lists", "list renamed")
}

// handleAdminRotateList replaces the key without moving videos: every copy
// of the old link stops working at once.
func (a *application) handleAdminRotateList(w http.ResponseWriter, r *http.Request) {
	if !a.parseListForm(w, r) {
		return
	}
	id := strings.TrimSpace(r.PostForm.Get("id"))
	if !store.ValidAccessListID(id) {
		adminRedirectError(w, r, "/admin/lists", "invalid list")
		return
	}
	l, err := a.fs.GetAccessList(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrInvalid) {
			adminRedirectError(w, r, "/admin/lists", "list not found")
		} else {
			a.apiServerError(w, r, "read access list", err)
		}
		return
	}
	raw, prefix, hash, err := store.GenerateAccessKey()
	if err != nil {
		a.apiServerError(w, r, "generate list key", err)
		return
	}
	l.Prefix, l.KeyHash = prefix, hash
	if err := a.fs.PutAccessList(l); err != nil {
		a.apiServerError(w, r, "rotate list key", err)
		return
	}
	log.Printf("admin %q rotated key for access list %q (%s)", currentUser(r).Username, l.Name, id)
	a.renderAdminLists(w, r, map[string]any{"NewListKey": raw, "NewListName": l.Name, "NewListID": id})
}

// handleAdminDeleteList refuses to delete a list that still holds videos:
// fail closed rather than silently publishing them.
func (a *application) handleAdminDeleteList(w http.ResponseWriter, r *http.Request) {
	if !a.parseListForm(w, r) {
		return
	}
	id := strings.TrimSpace(r.PostForm.Get("id"))
	if !store.ValidAccessListID(id) {
		adminRedirectError(w, r, "/admin/lists", "invalid list")
		return
	}
	videos, err := a.fs.List("")
	if err != nil {
		a.apiServerError(w, r, "list videos", err)
		return
	}
	held := 0
	for _, m := range videos {
		if m.AccessList == id {
			held++
		}
	}
	if held > 0 {
		adminRedirectError(w, r, "/admin/lists", "list still holds videos; move them to another list or public first")
		return
	}
	if err := a.fs.DeleteAccessList(id); err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrInvalid) {
			adminRedirectError(w, r, "/admin/lists", "list not found")
		} else {
			a.apiServerError(w, r, "delete access list", err)
		}
		return
	}
	log.Printf("admin %q deleted access list %q", currentUser(r).Username, id)
	adminRedirectNotice(w, r, "/admin/lists", "list deleted")
}
