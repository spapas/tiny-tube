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

// apiKeyTouchInterval bounds last_used_at writes: a busy client must not cause
// a metadata write on every authenticated request.
const apiKeyTouchInterval = 5 * time.Minute

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
	now := time.Now()
	if last, err := time.Parse(time.RFC3339Nano, k.LastUsedAt); err != nil || now.Sub(last) >= apiKeyTouchInterval {
		if err := a.fs.TouchAPIKey(prefix, now); err != nil {
			log.Printf("touch API key %q: %v", prefix, err)
		}
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
		adminRedirectError(w, r, "/admin/keys", "error.keyName")
		return
	}
	if _, err := a.fs.GetUser(username); err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "/admin/keys", "error.userNotFound")
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
		writeAPIError(w, r, http.StatusBadRequest, "error.invalidForm")
		return
	}
	prefix := strings.TrimSpace(r.PostForm.Get("prefix"))
	if !store.ValidKeyPrefix(prefix) {
		writeAPIError(w, r, http.StatusBadRequest, "error.invalidKey")
		return
	}
	if err := a.fs.DeleteAPIKey(prefix); err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "/admin/keys", "error.keyNotFound")
			return
		}
		a.apiServerError(w, r, "revoke key", err)
		return
	}
	log.Printf("admin %q revoked API key %q", currentUser(r).Username, prefix)
	adminRedirectNotice(w, r, "/admin/keys", "notice.keyRevoked")
}

// renderAdmin renders the key dashboard; extracted so key creation can show
// the plaintext once instead of redirecting.
func (a *application) renderAdmin(w http.ResponseWriter, r *http.Request, extra map[string]any) {
	data, err := a.adminData(r, "keys")
	if err != nil {
		a.serverError(w, "admin data", err)
		return
	}
	for k, v := range extra {
		data[k] = v
	}
	a.renderPage(w, r, adminKeysTmpl, data)
}
