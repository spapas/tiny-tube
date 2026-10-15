package main

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"tiny-tube/store"
)

// Admin area: service overview plus local user, role and API key
// management on separate pages. LDAP users are provisioned on first login;
// all role grants happen here.

func (a *application) adminData(r *http.Request, section string) (map[string]any, error) {
	users, err := a.fs.ListUsers()
	if err != nil {
		return nil, err
	}
	videos, err := a.fs.List("")
	if err != nil {
		return nil, err
	}
	byStatus := map[string]int{}
	for _, m := range videos {
		byStatus[m.Status]++
	}
	keys, err := a.fs.ListAPIKeys()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"Page": "admin", "Title": "Διαχείριση", "AdminSection": section,
		"Users": users, "VideoTotal": len(videos), "ByStatus": byStatus,
		"StorageOK": checkWritable(a.videoDir) == nil,
		"FFmpeg":    a.ffmpegBin, "Error": r.URL.Query().Get("error"),
		"Notice": r.URL.Query().Get("notice"), "APIKeys": keys,
	}, nil
}

func (a *application) handleAdmin(w http.ResponseWriter, r *http.Request) {
	data, err := a.adminData(r, "overview")
	if err != nil {
		a.serverError(w, "admin data", err)
		return
	}
	if _, err := a.fs.List(""); err != nil {
		data["MetadataOK"] = false
	} else {
		data["MetadataOK"] = true
	}
	a.renderPage(w, r, adminTmpl, data)
}

func (a *application) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	data, err := a.adminData(r, "users")
	if err != nil {
		a.serverError(w, "admin data", err)
		return
	}
	a.renderPage(w, r, adminUsersTmpl, data)
}

func (a *application) handleAdminUserNew(w http.ResponseWriter, r *http.Request) {
	data, err := a.adminData(r, "user-new")
	if err != nil {
		a.serverError(w, "admin data", err)
		return
	}
	a.renderPage(w, r, adminUserNewTmpl, data)
}

func (a *application) handleAdminKeys(w http.ResponseWriter, r *http.Request) {
	data, err := a.adminData(r, "keys")
	if err != nil {
		a.serverError(w, "admin data", err)
		return
	}
	a.renderPage(w, r, adminKeysTmpl, data)
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

func adminRedirectNotice(w http.ResponseWriter, r *http.Request, target, notice string) {
	http.Redirect(w, r, target+"?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}

func adminRedirectError(w http.ResponseWriter, r *http.Request, target, notice string) {
	http.Redirect(w, r, target+"?error="+url.QueryEscape(notice), http.StatusSeeOther)
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
		adminRedirectError(w, r, "/admin/users", "unknown role")
		return
	}
	if strings.TrimSpace(password) == "" {
		adminRedirectError(w, r, "/admin/users", "password required")
		return
	}
	if _, err := a.fs.GetUser(username); err == nil {
		adminRedirectError(w, r, "/admin/users", "user already exists")
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
	adminRedirectNotice(w, r, "/admin/users", "user created")
}

func (a *application) handleAdminSetRole(w http.ResponseWriter, r *http.Request) {
	if !a.parseAdminForm(w, r) {
		return
	}
	username, role := r.PostForm.Get("username"), r.PostForm.Get("role")
	if !store.ValidRole(role) {
		adminRedirectError(w, r, "/admin/users", "unknown role")
		return
	}
	me := currentUser(r).Username
	if username == me && role != store.RoleAdmin {
		adminRedirectError(w, r, "/admin/users", "cannot remove your own administrator role")
		return
	}
	u, err := a.fs.GetUser(username)
	if err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "/admin/users", "user not found")
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
			adminRedirectError(w, r, "/admin/users", "at least one active administrator is required")
			return
		}
	}
	u.Role = role
	if err := a.fs.PutUser(u); err != nil {
		a.apiServerError(w, r, "update role", err)
		return
	}
	log.Printf("admin %q set %q role to %s", me, username, role)
	adminRedirectNotice(w, r, "/admin/users", "role updated")
}

func (a *application) handleAdminSetActive(w http.ResponseWriter, r *http.Request) {
	if !a.parseAdminForm(w, r) {
		return
	}
	username := r.PostForm.Get("username")
	active := r.PostForm.Get("active") == "1"
	me := currentUser(r).Username
	if username == me && !active {
		adminRedirectError(w, r, "/admin/users", "cannot deactivate your own account")
		return
	}
	u, err := a.fs.GetUser(username)
	if err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "/admin/users", "user not found")
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
			adminRedirectError(w, r, "/admin/users", "at least one active administrator is required")
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
	adminRedirectNotice(w, r, "/admin/users", "user updated")
}

func (a *application) handleAdminSetPassword(w http.ResponseWriter, r *http.Request) {
	if !a.parseAdminForm(w, r) {
		return
	}
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	if strings.TrimSpace(password) == "" {
		adminRedirectError(w, r, "/admin/users", "password required")
		return
	}
	u, err := a.fs.GetUser(username)
	if err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "/admin/users", "user not found")
			return
		}
		a.apiServerError(w, r, "read user", err)
		return
	}
	if u.AuthSource != store.AuthLocal {
		adminRedirectError(w, r, "/admin/users", "LDAP users authenticate at the directory")
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
	adminRedirectNotice(w, r, "/admin/users", "password reset; user sessions revoked")
}

func (a *application) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !a.parseAdminForm(w, r) {
		return
	}
	username := r.PostForm.Get("username")
	me := currentUser(r).Username
	if username == me {
		adminRedirectError(w, r, "/admin/users", "cannot delete your own account")
		return
	}
	u, err := a.fs.GetUser(username)
	if err != nil {
		if os.IsNotExist(err) {
			adminRedirectError(w, r, "/admin/users", "user not found")
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
			adminRedirectError(w, r, "/admin/users", "at least one active administrator is required")
			return
		}
	}
	_ = a.fs.DeleteUserSessions(username)
	if err := a.fs.DeleteUser(username); err != nil {
		a.apiServerError(w, r, "delete user", err)
		return
	}
	log.Printf("admin %q deleted user %q", me, username)
	adminRedirectNotice(w, r, "/admin/users", "user deleted")
}
