package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"tiny-tube/store"
)

// bearerKey provisions an editor with an API key and returns the plaintext.
func bearerKey(t *testing.T, a *application, username, role string) string {
	t.Helper()
	if err := a.fs.PutUser(store.User{
		Username: username, DisplayName: username,
		AuthSource: store.AuthLocal, Role: role, Active: true,
	}); err != nil {
		t.Fatal(err)
	}
	raw, prefix, hash, err := store.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.fs.CreateAPIKey(store.APIKey{
		Prefix: prefix, Username: username, Name: "test", KeyHash: hash,
	}); err != nil {
		t.Fatal(err)
	}
	return raw
}

func bearerRequest(method, target, key string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Accept", "application/json")
	return r
}

func TestBearerAuthorizesLikeSession(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	raw := bearerKey(t, a, "auto", store.RoleEditor)

	// Protected browser page works with a Bearer token.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, bearerRequest("GET", "/upload", raw))
	if w.Code != 200 {
		t.Fatalf("bearer upload page: %d", w.Code)
	}

	// Unknown and malformed tokens stay anonymous: JSON 401 on writes.
	for _, key := range []string{"tt-00000000000000000000000000000000", "garbage", ""} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, bearerRequest("POST", "/api/edit", key))
		if w.Code != 401 {
			t.Fatalf("bad key %q: %d, want 401", key, w.Code)
		}
	}

	// A viewer key authenticates but is forbidden from editor routes.
	viewer := bearerKey(t, a, "watcher", store.RoleViewer)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, bearerRequest("POST", "/api/edit", viewer))
	if w.Code != 403 {
		t.Fatalf("viewer key: %d, want 403", w.Code)
	}

	// Successful use records last use without changing the hash.
	k, err := a.fs.GetAPIKey(strings.TrimPrefix(raw, "tt-")[:12])
	if err != nil || k.LastUsedAt == "" {
		t.Fatalf("last use not recorded: %+v %v", k, err)
	}
}

func TestBearerDiesWithUser(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	raw := bearerKey(t, a, "temp", store.RoleEditor)

	u, err := a.fs.GetUser("temp")
	if err != nil {
		t.Fatal(err)
	}
	u.Active = false
	if err := a.fs.PutUser(u); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, bearerRequest("POST", "/api/edit", raw))
	if w.Code != 401 {
		t.Fatalf("deactivated key: %d, want 401", w.Code)
	}

	if err := a.fs.DeleteUser("temp"); err != nil {
		t.Fatal(err)
	}
	if keys, err := a.fs.ListAPIKeys(); err != nil || len(keys) != 0 {
		t.Fatalf("keys survived deletion: %+v %v", keys, err)
	}
}

func TestAdminCreatesAndRevokesKeys(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	cookie := testSession(t, a, "tester")

	// Creation renders the plaintext once (never in a redirect URL).
	w, r := postForm(t, "/api/admin/users/keys/create", url.Values{"username": {"tester"}, "name": {"nightly"}})
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "tt-") {
		t.Fatalf("create key: %d", w.Code)
	}
	keys, err := a.fs.ListAPIKeys()
	if err != nil || len(keys) != 1 || keys[0].Username != "tester" {
		t.Fatalf("stored keys: %+v %v", keys, err)
	}
	if strings.Contains(w.Body.String(), keys[0].KeyHash) {
		t.Fatal("key hash leaked into the page")
	}

	// Unknown users and bad names are refused.
	for _, v := range []url.Values{
		{"username": {"ghost"}, "name": {"x"}},
		{"username": {"tester"}, "name": {""}},
	} {
		w, r = postForm(t, "/api/admin/users/keys/create", v)
		r.AddCookie(cookie)
		h.ServeHTTP(w, r)
		if w.Code == 200 && strings.Contains(w.Body.String(), "tt-") {
			t.Fatalf("bad create accepted: %v", v)
		}
		if n, _ := a.fs.ListAPIKeys(); len(n) != 1 {
			t.Fatalf("bad create stored a key: %v", v)
		}
	}

	// Revocation sticks; the old token goes anonymous.
	w, r = postForm(t, "/api/admin/users/keys/revoke", url.Values{"prefix": {keys[0].Prefix}})
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("revoke: %d", w.Code)
	}
	if keys, _ := a.fs.ListAPIKeys(); len(keys) != 0 {
		t.Fatalf("key survived revocation: %+v", keys)
	}
}
