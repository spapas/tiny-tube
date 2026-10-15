package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"tiny-tube/store"
)

type fakeDirectory struct {
	users map[string]string // username -> password
}

func (f *fakeDirectory) authenticate(username, password string) (ldapUser, error) {
	if want, ok := f.users[username]; !ok || want != password {
		return ldapUser{}, errInvalidCredentials
	}
	return ldapUser{Username: username, FirstName: "Ldap", LastName: "User", Email: username + "@example.com"}, nil
}

func TestHTMLLoginFlow(t *testing.T) {
	a := testApplication(t)
	h := a.routes()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/login", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `name="password"`) {
		t.Fatalf("login page: %d", w.Code)
	}

	// Wrong password re-renders with a vague error and no session.
	w, r := postForm(t, "/login", url.Values{"username": {"tester"}, "password": {"wrong"}})
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Λάθος στοιχεία") {
		t.Fatalf("bad login: %d %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("failed login issued a cookie")
	}

	// Correct password redirects and sets a secure-by-default cookie.
	w, r = postForm(t, "/login?next=/upload", url.Values{"username": {"tester"}, "password": {"test-password"}, "next": {"/upload"}})
	h.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "/upload" {
		t.Fatalf("good login: %d %q", w.Code, w.Header().Get("Location"))
	}
	var session *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Path != "/" {
		t.Fatalf("weak session cookie: %+v", session)
	}

	// Open redirects are rejected.
	w, r = postForm(t, "/login", url.Values{"username": {"tester"}, "password": {"test-password"}, "next": {"https://evil.example/"}})
	h.ServeHTTP(w, r)
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Fatalf("open redirect: %q", loc)
	}

	// The session reaches protected pages and the header shows the user.
	r = httptest.NewRequest("GET", "/upload", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "tester") {
		t.Fatalf("session upload page: %d", w.Code)
	}

	// Logout revokes the session.
	r = httptest.NewRequest("POST", "/logout", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("logout: %d", w.Code)
	}
	r = httptest.NewRequest("GET", "/upload", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("revoked session still valid: %d", w.Code)
	}
}

func TestJSONLoginForAutomation(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	cookie := loginRequest(t, h, "tester", "test-password")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/videos", nil)
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("automation session: %d", w.Code)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"tester","password":"wrong"}`))
	h.ServeHTTP(w, r)
	if w.Code != 401 || decodeJSON(t, w)["error"] == "" {
		t.Fatalf("bad JSON login: %d %s", w.Code, w.Body.String())
	}
}

func TestInactiveUsersCannotLogin(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	testUser(t, a, "sleepy", store.RoleEditor)
	u, _ := a.fs.GetUser("sleepy")
	u.Active = false
	if err := a.fs.PutUser(u); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"sleepy","password":"test-password"}`))
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("inactive login: %d", w.Code)
	}
}

func TestExpiredSessionsAreAnonymous(t *testing.T) {
	a := testApplication(t)
	raw := "expired-raw-token"
	sum := hashSessionToken(raw)
	if err := a.fs.CreateSession(store.Session{TokenHash: sum, Username: "tester", ExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/upload", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
	if u := a.validateSession(r); u != nil {
		t.Fatalf("expired session validated: %+v", u)
	}
}

func TestLDAPProvisioningAndLocalFallback(t *testing.T) {
	old := newDirectory
	defer func() { newDirectory = old }()
	newDirectory = func(config) (directory, error) {
		return &fakeDirectory{users: map[string]string{"ldap.ed": "ldap-pass"}}, nil
	}
	a := testApplication(t)
	a.cfg.AuthMode = "both"
	h := a.routes()

	// LDAP user provisions on first login as viewer.
	cookie := loginRequest(t, h, "ldap.ed", "ldap-pass")
	u, err := a.fs.GetUser("ldap.ed")
	if err != nil || u.AuthSource != store.AuthLDAP || u.Role != store.RoleViewer || u.PasswordHash != "" {
		t.Fatalf("provisioned LDAP user: %+v %v", u, err)
	}
	if u.DisplayName == "" {
		t.Fatal("LDAP display name not mapped")
	}
	r := httptest.NewRequest("GET", "/upload", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("fresh LDAP user upload: %d", w.Code)
	}

	// Local users still work in both mode.
	loginRequest(t, h, "tester", "test-password")

	// Wrong LDAP password falls back to local and fails vaguely.
	w = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"ldap.ed","password":"nope"}`))
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("bad LDAP password: %d", w.Code)
	}

	// ldap-only mode rejects unknown users without touching local.
	a.cfg.AuthMode = "ldap"
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"tester","password":"test-password"}`))
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("ldap mode accepted local user: %d", w.Code)
	}
}

func TestAdminManagesUsersAndKeepsOneAdmin(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	admin := loginRequest(t, h, "tester", "test-password")
	post := func(path string, v url.Values) *httptest.ResponseRecorder {
		w, r := postForm(t, path, v)
		r.AddCookie(admin)
		h.ServeHTTP(w, r)
		return w
	}

	// Create editor, grant nothing destructive by default.
	if w := post("/api/admin/users/create", url.Values{"username": {"ed2"}, "password": {"secret123"}, "role": {"editor"}}); w.Code != 303 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	editor := loginRequest(t, h, "ed2", "secret123")

	// Editor cannot touch admin APIs.
	w, r := postForm(t, "/api/admin/users/create", url.Values{"username": {"x"}, "password": {"y"}, "role": {"viewer"}})
	r.AddCookie(editor)
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("editor admin API: %d", w.Code)
	}

	// Self-demotion, self-deactivation and self-deletion are refused.
	for path, v := range map[string]url.Values{
		"/api/admin/users/role":   {"username": {"tester"}, "role": {"editor"}},
		"/api/admin/users/active": {"username": {"tester"}, "active": {"0"}},
		"/api/admin/users/delete": {"username": {"tester"}},
	} {
		if w := post(path, v); w.Code != 303 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if u, _ := a.fs.GetUser("tester"); !u.IsAdmin() || !u.Active {
		t.Fatalf("self-demotion applied: %+v", u)
	}

	// Last-admin protection for the second admin too.
	if w := post("/api/admin/users/create", url.Values{"username": {"admin2"}, "password": {"secret123"}, "role": {"admin"}}); w.Code != 303 {
		t.Fatalf("create admin2: %d", w.Code)
	}
	if w := post("/api/admin/users/role", url.Values{"username": {"admin2"}, "role": {"viewer"}}); w.Code != 303 {
		t.Fatalf("demote admin2: %d", w.Code)
	}
	if u, _ := a.fs.GetUser("tester"); !u.IsAdmin() {
		t.Fatal("tester lost admin while admin2 existed")
	}

	// Password reset revokes the editor session and the new password works.
	if w := post("/api/admin/users/password", url.Values{"username": {"ed2"}, "password": {"rotated"}}); w.Code != 303 {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/upload", nil)
	r.AddCookie(editor)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("old session survived reset: %d", w.Code)
	}
	loginRequest(t, h, "ed2", "rotated")

	// Deactivation blocks login.
	if w := post("/api/admin/users/active", url.Values{"username": {"ed2"}, "active": {"0"}}); w.Code != 303 {
		t.Fatalf("deactivate: %d", w.Code)
	}
	w = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"ed2","password":"rotated"}`))
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("deactivated login: %d", w.Code)
	}

	// Delete removes the user and their sessions.
	if w := post("/api/admin/users/delete", url.Values{"username": {"ed2"}}); w.Code != 303 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, err := a.fs.GetUser("ed2"); !os.IsNotExist(err) {
		t.Fatalf("deleted user readable: %v", err)
	}

	// Deleting the last remaining admin besides self is refused.
	if w := post("/api/admin/users/delete", url.Values{"username": {"admin2"}}); w.Code != 303 {
		t.Fatalf("delete admin2: %d", w.Code)
	}
	if _, err := a.fs.GetUser("tester"); err != nil {
		t.Fatal(err)
	}
}

func TestAdminDashboardShowsHealth(t *testing.T) {
	a := testApplication(t)
	h := a.routes()
	cookie := loginRequest(t, h, "tester", "test-password")

	r := httptest.NewRequest("GET", "/admin", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "/readyz") {
		t.Fatalf("admin overview: %d", w.Code)
	}

	for path, want := range map[string]string{
		"/admin/users":     "tester",
		"/admin/users/new": "new-username",
		"/admin/keys":      "key-username",
	} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}
