package main

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"tiny-tube/store"
)

// Admin area: site overview plus local user and role management. LDAP users
// are provisioned on first login; all role grants happen here.

func (a *application) handleAdmin(w http.ResponseWriter, r *http.Request) {
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
	storageOK := checkWritable(a.videoDir) == nil
	_, metaErr := a.fs.List("")
	a.renderPage(w, r, adminTmpl, map[string]any{
		"Page": "admin", "Title": "Διαχείριση",
		"Users": users, "VideoTotal": len(videos), "ByStatus": byStatus,
		"StorageOK": storageOK, "MetadataOK": metaErr == nil,
		"FFmpeg": a.ffmpegBin, "Error": r.URL.Query().Get("error"),
		"Notice": r.URL.Query().Get("notice"),
	})
}

func (a *application) parseAdminForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "invalid form")
		return false
	}
	if !store.ValidUsername(r.PostForm.Get("username")) {
		writeAPIError(w, r, http.StatusBadRequest, "invalid username")
		return false
	}
	return true
}

func adminRedirectNotice(w http.ResponseWriter, r *http.Request, notice string) {
	http.Redirect(w, r, "/admin?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}

func adminRedirectError(w http.ResponseWriter, r *http.Request, notice string) {
	http.Redirect(w, r, "/admin?error="+url.QueryEscape(notice), http.StatusSeeOther)
}

// lastAdminStanding reports whether removing admin rights from username would
// leave no active administrator.
func (a *application) lastAdminStanding(except string) (bool, error) {
	users, err := a.fs.ListUsers()
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if u.IsAdmin() && u.Active && u.Username != except {
			return false, nil
		}
	}
	return true, nil
}

func (a *application) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	if !a.parseAdminForm(w, r) {
		return
	}
	username := r.PostForm.Get("username")
	role := r.PostForm.Get("role")
	password := r.PostForm.Get("password")
	if !store.ValidRole(role) {
		adminRedirectError(w, r, "unknown role")
		return
	}
	if strings.TrimSpace(password) == "" {
		adminRedirectError(w, r, "password required")
		return
	}
	if _, err := a.fs.GetUser(username); err == nil {
		adminRedirectError(w, r, "user already exists")
		return
	} else if !os.IsNotExist(err) {
		a.apiServerError(w, r, "read user", err)
		return
	}
	hash, err := hashPassword(password)
	if err != nil {
		a.apiServerError(w, r, "hash password", err)
		return
	}
	display := strings.TrimSpace(r.PostForm.Get("display"))
	if display == "" {
		display = username
	}
	if err := a.fs.PutUser(store.User{
		Username: username, DisplayName: display, PasswordHash: hash,
		AuthSource: store.AuthLocal, Role: role, Active: true,
	}); err != nil {
		a.apiServerError(w, r, "create user", err)
		return
	}
	log.Printf("admin %q created user %q (%s)", currentUser(r).Username, username, role)
	adminRedirectNotice(w, r, "user created")
}

func (a *application) handleAdminSetRole(w http.ResponseWriter, r *http.Request) {
	if !a.parseAdminForm(w, r) {
		return
	}
	username, role := r.PostForm.Get("username"), r.PostForm.Get("role")
	if !store.ValidRole(role) {
		adminRedirectError(w, r, "unknown role")
		return
	}
	me := currentUser(r).Username
	if username == me && role != store.RoleAdmin {
		adminRedirectError(w, r, "cannot remove your own administrator role")
		return
	}
	u, err := a.fs.GetUser(username)
	if err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "user not found")
			return
		}
		a.apiServerError(w, r, "read user", err)
		return
	}
	if u.IsAdmin() && role != store.RoleAdmin {
		if last, err := a.lastAdminStanding(username); err != nil {
			a.apiServerError(w, r, "count administrators", err)
			return
		} else if last {
			adminRedirectError(w, r, "at least one active administrator is required")
			return
		}
	}
	u.Role = role
	if err := a.fs.PutUser(u); err != nil {
		a.apiServerError(w, r, "update role", err)
		return
	}
	log.Printf("admin %q set %q role to %s", me, username, role)
	adminRedirectNotice(w, r, "role updated")
}

func (a *application) handleAdminSetActive(w http.ResponseWriter, r *http.Request) {
	if !a.parseAdminForm(w, r) {
		return
	}
	username := r.PostForm.Get("username")
	active := r.PostForm.Get("active") == "1"
	me := currentUser(r).Username
	if username == me && !active {
		adminRedirectError(w, r, "cannot deactivate your own account")
		return
	}
	u, err := a.fs.GetUser(username)
	if err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "user not found")
			return
		}
		a.apiServerError(w, r, "read user", err)
		return
	}
	if u.IsAdmin() && !active {
		if last, err := a.lastAdminStanding(username); err != nil {
			a.apiServerError(w, r, "count administrators", err)
			return
		} else if last {
			adminRedirectError(w, r, "at least one active administrator is required")
			return
		}
	}
	u.Active = active
	if err := a.fs.PutUser(u); err != nil {
		a.apiServerError(w, r, "update user", err)
		return
	}
	if !active {
		_ = a.fs.DeleteUserSessions(username)
	}
	log.Printf("admin %q set %q active=%v", me, username, active)
	adminRedirectNotice(w, r, "user updated")
}

func (a *application) handleAdminSetPassword(w http.ResponseWriter, r *http.Request) {
	if !a.parseAdminForm(w, r) {
		return
	}
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	if strings.TrimSpace(password) == "" {
		adminRedirectError(w, r, "password required")
		return
	}
	u, err := a.fs.GetUser(username)
	if err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "user not found")
			return
		}
		a.apiServerError(w, r, "read user", err)
		return
	}
	if u.AuthSource != store.AuthLocal {
		adminRedirectError(w, r, "LDAP users authenticate at the directory")
		return
	}
	hash, err := hashPassword(password)
	if err != nil {
		a.apiServerError(w, r, "hash password", err)
		return
	}
	u.PasswordHash = hash
	if err := a.fs.PutUser(u); err != nil {
		a.apiServerError(w, r, "update password", err)
		return
	}
	_ = a.fs.DeleteUserSessions(username)
	log.Printf("admin %q reset password for %q", currentUser(r).Username, username)
	adminRedirectNotice(w, r, "password reset; user sessions revoked")
}

func (a *application) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !a.parseAdminForm(w, r) {
		return
	}
	username := r.PostForm.Get("username")
	me := currentUser(r).Username
	if username == me {
		adminRedirectError(w, r, "cannot delete your own account")
		return
	}
	u, err := a.fs.GetUser(username)
	if err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "user not found")
			return
		}
		a.apiServerError(w, r, "read user", err)
		return
	}
	if u.IsAdmin() {
		if last, err := a.lastAdminStanding(username); err != nil {
			a.apiServerError(w, r, "count administrators", err)
			return
		} else if last {
			adminRedirectError(w, r, "at least one active administrator is required")
			return
		}
	}
	_ = a.fs.DeleteUserSessions(username)
	if err := a.fs.DeleteUser(username); err != nil {
		a.apiServerError(w, r, "delete user", err)
		return
	}
	log.Printf("admin %q deleted user %q", me, username)
	adminRedirectNotice(w, r, "user deleted")
}
