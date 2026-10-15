package main

import (
	"crypto/subtle"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"tiny-tube/store"
)

// API keys are long-lived automation credentials ("tt-" + 32 base64url
// characters). They authorize exactly like session cookies — roles still come
// from the local user row — but travel in the Authorization header so scripts
// do not juggle cookies:
//
//	Authorization: Bearer tt-....
//
// Only the SHA-256 of the key is stored; the plaintext is shown once at
// creation. Keys never expire; revoke them when they leak or rotate staff.
// Deactivating or deleting the user disables its keys as well.

// validateAPIKey resolves a Bearer token to its active user. Unknown,
// mismatched or orphaned keys stay anonymous, like a stale session cookie.
func (a *application) validateAPIKey(r *http.Request) *store.User {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return nil
	}
	prefix, ok := store.SplitAPIKey(raw)
	if !ok {
		return nil
	}
	k, err := a.fs.GetAPIKey(prefix)
	if err != nil {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(k.KeyHash), []byte(store.HashAPIKey(strings.TrimSpace(raw)))) != 1 {
		return nil
	}
	u, err := a.fs.GetUser(k.Username)
	if err != nil || !u.Active {
		return nil
	}
	if err := a.fs.TouchAPIKey(prefix, time.Now()); err != nil {
		log.Printf("touch API key %q: %v", prefix, err)
	}
	return &u
}

func (a *application) handleAdminCreateKey(w http.ResponseWriter, r *http.Request) {
	if !a.parseAdminForm(w, r) {
		return
	}
	username := r.PostForm.Get("username")
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if name == "" || len(name) > 100 {
		adminRedirectError(w, r, "key name required (up to 100 characters)")
		return
	}
	if _, err := a.fs.GetUser(username); err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "user not found")
			return
		}
		a.apiServerError(w, r, "read user", err)
		return
	}
	raw, prefix, hash, err := store.GenerateAPIKey()
	if err != nil {
		a.apiServerError(w, r, "generate key", err)
		return
	}
	if err := a.fs.CreateAPIKey(store.APIKey{
		Prefix: prefix, Username: username, Name: name, KeyHash: hash,
	}); err != nil {
		a.apiServerError(w, r, "create key", err)
		return
	}
	log.Printf("admin %q created API key %q for %q", currentUser(r).Username, prefix, username)
	// Render (not redirect): the plaintext key must not land in URLs or logs.
	a.renderAdmin(w, r, map[string]any{"NewAPIKey": raw, "NewAPIKeyUser": username})
}

func (a *application) handleAdminRevokeKey(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "invalid form")
		return
	}
	prefix := strings.TrimSpace(r.PostForm.Get("prefix"))
	if !store.ValidKeyPrefix(prefix) {
		writeAPIError(w, r, http.StatusBadRequest, "invalid key")
		return
	}
	if err := a.fs.DeleteAPIKey(prefix); err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "key not found")
			return
		}
		a.apiServerError(w, r, "revoke key", err)
		return
	}
	log.Printf("admin %q revoked API key %q", currentUser(r).Username, prefix)
	adminRedirectNotice(w, r, "API key revoked")
}

// renderAdmin renders the dashboard; extracted so key creation can show the
// plaintext once instead of redirecting.
func (a *application) renderAdmin(w http.ResponseWriter, r *http.Request, extra map[string]any) {
	users, err := a.fs.ListUsers()
	if err != nil {
		a.serverError(w, "list users", err)
		return
	}
	videos, err := a.fs.List("")
	if err != nil {
		a.serverError(w, "list videos", err)
		return
	}
	byStatus := map[string]int{}
	for _, m := range videos {
		byStatus[m.Status]++
	}
	keys, err := a.fs.ListAPIKeys()
	if err != nil {
		a.serverError(w, "list API keys", err)
		return
	}
	data := map[string]any{
		"Page": "admin", "Title": "Διαχείριση",
		"Users": users, "VideoTotal": len(videos), "ByStatus": byStatus,
		"StorageOK": checkWritable(a.videoDir) == nil,
		"Error":     r.URL.Query().Get("error"),
		"Notice":    r.URL.Query().Get("notice"),
		"APIKeys":   keys,
	}
	for k, v := range extra {
		data[k] = v
	}
	a.renderPage(w, r, adminTmpl, data)
}
