package main

import (
	"log"
	"net/http"
	"time"

	"tiny-tube/store"
)

// Stable audit action identifiers. They are stored and displayed verbatim,
// never translated, so log analysis does not depend on the interface language.
const (
	auditVideoUpload    = "video.upload"
	auditVideoEdit      = "video.edit"
	auditVideoDelete    = "video.delete"
	auditVideoRetry     = "video.retry"
	auditSubtitleUpload = "subtitle.upload"
	auditSubtitleDelete = "subtitle.delete"
	auditLogin          = "auth.login"
	auditLogout         = "auth.logout"
	auditUserCreate     = "user.create"
	auditUserRole       = "user.role"
	auditUserActive     = "user.active"
	auditUserPassword   = "user.password"
	auditUserDelete     = "user.delete"
	auditAPIKeyCreate   = "apikey.create"
	auditAPIKeyRevoke   = "apikey.revoke"
	auditListCreate     = "list.create"
	auditListRename     = "list.rename"
	auditListRotate     = "list.rotate"
	auditListDelete     = "list.delete"
	auditPlaylistCreate = "playlist.create"
	auditPlaylistRename = "playlist.rename"
	auditPlaylistDelete = "playlist.delete"
	auditPlaylistItems  = "playlist.items"
)

// audit records an operator action under the current request user. Audit
// failures are logged but never fail the request itself.
func (a *application) audit(r *http.Request, action, target, detail string) {
	actor := "anonymous"
	if u := currentUser(r); u != nil {
		actor = u.Username
	}
	a.auditAs(actor, action, target, detail)
}

func (a *application) auditAs(actor, action, target, detail string) {
	if err := a.fs.AppendAudit(store.AuditEntry{
		Time:   time.Now().UTC().Format(time.RFC3339Nano),
		Actor:  actor,
		Action: action,
		Target: target,
		Detail: truncateAuditDetail(detail),
	}); err != nil {
		log.Printf("audit %s %s: %v", action, target, err)
	}
}

func truncateAuditDetail(detail string) string {
	const maxDetailRunes = 200
	runes := []rune(detail)
	if len(runes) <= maxDetailRunes {
		return detail
	}
	return string(runes[:maxDetailRunes]) + "…"
}
