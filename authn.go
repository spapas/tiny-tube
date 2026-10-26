package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"tiny-tube/store"
)

// Session cookies hold an opaque random token; only its SHA-256 is persisted,
// so a database leak does not hijack sessions. Expiry slides on every
// validated request, mirroring the SHDE model.
const sessionCookieName = "tiny_tube_session"

const sessionTTL = 24 * time.Hour
const sessionRefreshInterval = 5 * time.Minute

type contextKey int

const userContextKey contextKey = iota

func hashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func checkPassword(hash, password string) bool {
	if hash == "" || password == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func hashSessionToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// currentUser returns the authenticated user attached to the request, or nil.
func currentUser(r *http.Request) *store.User {
	if u, ok := r.Context().Value(userContextKey).(*store.User); ok {
		return u
	}
	return nil
}

func contextWithUser(r *http.Request, u *store.User) context.Context {
	return context.WithValue(r.Context(), userContextKey, u)
}

// renderPage renders with the current user attached for navigation/role UI.
func (a *application) renderPage(w http.ResponseWriter, r *http.Request, tmpl *template.Template, data map[string]any) {
	data["CurrentUser"] = currentUser(r)
	lang := requestLanguage(r)
	data["Lang"] = lang
	data["Languages"] = appTranslations.languages
	data["LanguageReturn"] = r.URL.RequestURI()
	// A mutation may render a page (e.g. a newly issued API/list key). The
	// language form must return to the corresponding GET page, never that POST.
	if r.Method != http.MethodGet {
		switch tmpl.Name() {
		case "admin-lists":
			data["LanguageReturn"] = "/admin/lists"
		case "admin-keys":
			data["LanguageReturn"] = "/admin/keys"
		case "edit":
			data["LanguageReturn"] = "/edit?id=" + url.QueryEscape(r.FormValue("id"))
		default:
			data["LanguageReturn"] = "/login?next=" + url.QueryEscape(r.FormValue("next"))
		}
	}
	for _, key := range []string{"Error", "Notice"} {
		if value, ok := data[key].(string); ok {
			data[key] = translate(lang, value)
		}
	}
	w.Header().Set("Content-Language", lang)
	render(w, languageTemplates[lang].Lookup(tmpl.Name()), data)
}

// issueSession creates a sliding session and writes the cookie.
func (a *application) issueSession(w http.ResponseWriter, username string) error {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return err
	}
	raw := base64.RawURLEncoding.EncodeToString(entropy[:])
	sess := store.Session{
		TokenHash: hashSessionToken(raw),
		Username:  username,
		ExpiresAt: time.Now().Add(sessionTTL).UTC().Format(time.RFC3339Nano),
	}
	if err := a.fs.CreateSession(sess); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: raw, Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: a.cfg.CookieSecure,
	})
	return nil
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
}

// validateSession resolves the session cookie to an active user and slides
// its expiry. Stale sessions for missing, inactive or renamed users are
// revoked and treated as anonymous.
func (a *application) validateSession(r *http.Request) *store.User {
	return a.validateSessionResponse(nil, r)
}

func (a *application) validateSessionResponse(w http.ResponseWriter, r *http.Request) *store.User {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	tokenHash := hashSessionToken(cookie.Value)
	sess, err := a.fs.GetSession(tokenHash)
	if err != nil {
		return nil
	}
	u, err := a.fs.GetUser(sess.Username)
	if err != nil || !u.Active {
		_ = a.fs.DeleteSession(tokenHash)
		return nil
	}
	now := time.Now()
	expires, _ := time.Parse(time.RFC3339Nano, sess.ExpiresAt)
	if expires.Sub(now) <= sessionTTL-sessionRefreshInterval {
		if err := a.fs.RefreshSession(sess, now.Add(sessionTTL).UTC().Format(time.RFC3339Nano)); err != nil {
			return nil
		}
		if w != nil {
			http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: cookie.Value, Path: "/",
				MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.cfg.CookieSecure})
		}
	}
	return &u
}

func (a *application) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := a.validateSessionResponse(w, r); u != nil {
			r = r.WithContext(contextWithUser(r, u))
		} else if u := a.validateAPIKey(r); u != nil {
			r = r.WithContext(contextWithUser(r, u))
		}
		next.ServeHTTP(w, r)
	})
}

// limited applies the authentication rate limit before any credential or
// session check, so 429 responses cannot become oracles.
func (a *application) limited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allowed, retry := a.authRequests.allow(r.RemoteAddr); !allowed {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeAPIError(w, r, http.StatusTooManyRequests, "error.authRateLimit")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *application) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u == nil {
			w.Header().Set("Cache-Control", "no-store")
			if wantsJSON(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireEditor permits editors and admins; requireAdmin permits admins only.
// Deletion stays admin-only by decision; upload and metadata edits are shared.
func (a *application) requireEditor(next http.Handler) http.Handler {
	return a.requireLogin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).CanEdit() {
			writeAPIError(w, r, http.StatusForbidden, "error.editorRequired")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (a *application) requireAdmin(next http.Handler) http.Handler {
	return a.requireLogin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin() {
			writeAPIError(w, r, http.StatusForbidden, "error.adminRequired")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// authenticate routes by AUTH_MODE: local users, LDAP, or LDAP with local
// fallback. Errors stay vague to avoid user enumeration.
func (a *application) authenticate(username, password string) (store.User, error) {
	var empty store.User
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return empty, errInvalidCredentials
	}
	switch a.cfg.AuthMode {
	case "ldap":
		return a.viaLDAP(username, password)
	case "both":
		// Local identities never enter the LDAP domain, including during outages.
		if u, err := a.fs.GetUser(username); err == nil && u.AuthSource == store.AuthLocal {
			return a.viaLocal(username, password)
		} else if err != nil && !os.IsNotExist(err) && !errors.Is(err, os.ErrInvalid) {
			return empty, err
		}
		return a.viaLDAP(username, password)
	default:
		return a.viaLocal(username, password)
	}
}

var errInvalidCredentials = errors.New("invalid credentials")

func (a *application) viaLocal(username, password string) (store.User, error) {
	var empty store.User
	u, err := a.fs.GetUser(username)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, os.ErrInvalid) {
			// Dummy compare keeps timing close for missing users.
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), []byte(password))
			return empty, errInvalidCredentials
		}
		return empty, err
	}
	if u.AuthSource != store.AuthLocal || !checkPassword(u.PasswordHash, password) {
		return empty, errInvalidCredentials
	}
	if !u.Active {
		return empty, errInvalidCredentials
	}
	a.touchLogin(u)
	return u, nil
}

func (a *application) viaLDAP(username, password string) (store.User, error) {
	var empty store.User
	dir, err := newDirectory(a.cfg)
	if err != nil {
		return empty, err
	}
	found, err := dir.authenticate(username, password)
	if err != nil {
		return empty, err
	}
	u, err := a.upsertLDAPUser(found)
	if err != nil {
		return empty, err
	}
	if !u.Active {
		return empty, errInvalidCredentials
	}
	a.touchLogin(u)
	return u, nil
}

// upsertLDAPUser provisions the LDAP identity locally. Roles stay local:
// new users arrive as viewers and an administrator grants editor/admin.
func (a *application) upsertLDAPUser(found ldapUser) (store.User, error) {
	a.userMu.Lock()
	defer a.userMu.Unlock()
	var empty store.User
	u, err := a.fs.GetUser(found.Username)
	if err != nil && !os.IsNotExist(err) {
		return empty, err
	}
	if os.IsNotExist(err) {
		u = store.User{Username: found.Username, Role: store.RoleViewer, Active: true}
	} else if u.AuthSource != store.AuthLDAP {
		// Account linking must be explicit; never adopt local roles or keys.
		return empty, errInvalidCredentials
	} else if a.cfg.LDAPAlwaysUpdate {
	} else {
		return u, nil
	}
	u.AuthSource = store.AuthLDAP
	u.PasswordHash = ""
	if display := strings.TrimSpace(found.FirstName + " " + found.LastName); display != "" {
		u.DisplayName = display
	} else {
		u.DisplayName = found.Username
	}
	if err := a.fs.PutUser(u); err != nil {
		return empty, err
	}
	return u, nil
}

func (a *application) touchLogin(u store.User) {
	if err := a.fs.TouchUserLogin(u.Username, time.Now()); err != nil {
		log.Printf("record login for %q: %v", u.Username, err)
	}
}

// localRedirectTarget keeps post-login redirects on this site.
func localRedirectTarget(next string) string {
	if len(next) == 0 || next[0] != '/' || strings.HasPrefix(next, "//") {
		return "/"
	}
	for _, c := range next {
		if c == '\\' || c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	return next
}

func (a *application) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.renderPage(w, r, loginTmpl, map[string]any{
		"Page": "login", "Title": "auth.login",
		"Next": r.URL.Query().Get("next"), "Error": "",
	})
}

func (a *application) handleLogin(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		a.renderPage(w, r, loginTmpl, map[string]any{"Page": "login", "Title": "auth.login", "Error": "auth.invalidForm"})
		return
	}
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	u, err := a.authenticate(username, password)
	if err != nil {
		if !errors.Is(err, errInvalidCredentials) {
			a.apiServerError(w, r, "login", err)
			return
		}
		log.Printf("failed login for %q", username)
		a.renderPage(w, r, loginTmpl, map[string]any{
			"Page": "login", "Title": "auth.login",
			"Next": r.PostForm.Get("next"), "Error": "auth.invalidCredentials",
		})
		return
	}
	if err := a.issueSession(w, u.Username); err != nil {
		a.apiServerError(w, r, "login session", err)
		return
	}
	log.Printf("login succeeded for %q (%s)", u.Username, u.AuthSource)
	http.Redirect(w, r, localRedirectTarget(r.PostForm.Get("next")), http.StatusSeeOther)
}

// handleAPILogin keeps automation working without Basic Auth: JSON credentials
// in, the same session cookie out.
func (a *application) handleAPILogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username and password required"})
		return
	}
	u, err := a.authenticate(creds.Username, creds.Password)
	if err != nil {
		if !errors.Is(err, errInvalidCredentials) {
			a.apiServerError(w, r, "login", err)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	if err := a.issueSession(w, u.Username); err != nil {
		a.apiServerError(w, r, "login session", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username, "role": u.Role})
}

func (a *application) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		_ = a.fs.DeleteSession(hashSessionToken(cookie.Value))
	}
	clearSessionCookie(w, a.cfg.CookieSecure)
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]bool{"logged_out": true})
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ensureAdmin seeds the first administrator from ADMIN_USER/ADMIN_PASS when
// the user table is empty. Existing deployments keep working untouched;
// ADMIN_* settings are ignored once any user exists.
func (a *application) ensureAdmin() error {
	count, err := a.fs.UserCount()
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	username := strings.TrimSpace(a.cfg.AdminUser)
	password := a.cfg.AdminPass
	if !store.ValidUsername(username) {
		return errors.New("set ADMIN_USER to a valid username (letters, digits, . _ -)")
	}
	if strings.TrimSpace(password) == "" || strings.TrimSpace(password) == "changeme" {
		return errors.New("set ADMIN_PASS to a nonempty password other than changeme")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	if err := a.fs.PutUser(store.User{
		Username: username, PasswordHash: hash, AuthSource: store.AuthLocal,
		Role: store.RoleAdmin, Active: true,
	}); err != nil {
		return err
	}
	log.Printf("created initial administrator %q", username)
	return nil
}
