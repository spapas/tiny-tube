# Project review — 2026-10-24

Reviewed working tree: `9d15afb` (`hide upload controls and conversion status from viewers`).
Scope: authorization, locked lists, local/LDAP authentication, sessions, media,
management forms, client scripts and library scaling. The findings below describe
the original snapshot; the follow-up implementation is summarized here.

## Resolution follow-up — 2026-10-24

All five high-priority code findings are corrected, with focused regression
coverage. No real video directory, operator credentials or deployed instance was
changed. The original evidence is retained below as a historical record.

| Finding | Implemented correction |
| --- | --- |
| S1 | Missing metadata returns 404 and unexpected authorization-read errors return 503. Orphans are no longer publicly served. |
| S2 | Cache fills, metadata writes and deletions share the policy lock, preventing stale cache decisions after visibility changes. |
| S3 | Both stores provide refresh-only session updates, never inserting a revoked token. Refreshes are coalesced to five-minute intervals and renew the browser cookie. |
| S4 / A1 | LDAP rejects same-named local accounts. `both` routes existing local identities directly to local verification, independently of LDAP availability. Login timestamps no longer overwrite account fields. |
| S5 | Failed picker reads block upload/edit forms. Missing current-list options also block editing. Omitted `access_list` preserves the policy; explicit empty visibility makes it public. |
| S6 | Pages, APIs, newly displayed secrets and media carry no-store policies, with Cookie/Authorization variation. |
| P1 | An O(1) eviction ring enforces the 8,192-entry view cap; client IPs are hashed in the in-memory key. |
| P2 | HTML/locked-list pages default to 24 items and JSON to 50, capped at 100. SQLite performs authorized counts/filtering/order/paging in queries; readiness and queue reads avoid loading every video. FS still scans JSON by design. |
| U1 | Every matching deletion form gets a confirmation, with separate wording for users, lists and videos. |
| U2 | Locked watch pages offer copying only with a valid capability key; otherwise they explain how to obtain the original list link. |
| Smaller UX issues | Search preserves Popular sorting; Greek-byte validation keeps edit values and reports field errors. Long visibility selectors and tablet navigation have responsive CSS fixes. |

Compatibility notes: API consumers must iterate `page`/`page_size` instead of
assuming a single response contains the whole library. Existing LDAP role grants
are preserved; operators should review any accounts auto-adopted by older builds.
This fix prevents future source collisions and does not infer/revert historical
role grants. LDAP identities still use directory usernames; stable-object-ID and
rename/reuse management remain operator concerns.

Final verification:

- **228 Go tests/subtests pass, zero failures/skips**, including real FFmpeg
  HLS/poster integration, both storage backends and deterministic concurrency tests.
- **3 Node UI behavior tests pass** (`node --test tests/ui.test.cjs`).
- Changed Go files are formatted; `go vet ./...`, `go build ./...`, module checksum
  verification and JavaScript syntax checks pass.
- The 1,000-video payload regression now returns **41,501 bytes and 24 cards**,
  compared with the earlier 1,536,614-byte unpaginated fixture. This measures HTML
  size, not a production throughput or browser-rendering benchmark.
- Isolated HTTP checks pass for session login/role boundaries, multipart upload,
  locked original denial, capability-granted watch/Range/HLS/poster access, CSRF,
  title-only visibility preservation, key rotation/revocation, explicit publication,
  paginated search, deletion and logout. Original Range requests return 206.
- Browser verification reached video readyState 4. Library paging and long-list
  upload/edit forms fit the tested 320-pixel viewport after the selector fix.
  A 768-pixel navigation overflow was found and corrected in CSS; its final visual
  recheck was interrupted when the browser controller stalled on a native confirm
  dialog. Node tests verify every delete handler; no dialog was accepted and no
  browser-driven deletion was performed. No final screenshot/device matrix is
  claimed. Tablet/desktop visual checks remain a follow-up.
- FFmpeg absence/discovery and no-conversion handler behavior are covered by the
  automated suite. An extra separate-process no-FFmpeg launch was rejected by
  automatic command review without a detailed reason; that extra manual run is
  not reported as passed.
- The current pinned vulnerability scan is still **unavailable** because access
  to `vuln.go.dev` was denied. Race detection remains unavailable without CGO/a C
  compiler. Production TLS/Nginx and Linux runtime/load validation remain separate.

## Original assessment

## High priority: security and accidental publication

### S1. Media authorization fails open on metadata read errors

Location: `media.go:76`, `media.go:98`.

On a cache miss, `mediaAccessList` returns `ok=false` for unexpected storage
errors. `serveVideo` checks access only when `ok` is true and the list ID is
nonempty, then serves the file otherwise. A temporary database/read error can
therefore make a locked original, poster or HLS file public to a caller who
knows its path. Missing metadata is deliberately treated as a public orphan,
which also means loss of a locked video's metadata loses its privacy policy.

Reproduction: inject an unexpected `Get` failure while retaining a locked
original. Anonymous requests return **200 and 100,791 bytes**, with both FS
and SQLite. This is not a claim that an outsider can cause arbitrary DB errors.

Required correction: deny media access when metadata cannot be resolved; return
an appropriate unavailable/not-found response. Reconsider public orphan serving
now that metadata contains the authorization policy.

### S2. A concurrent cache fill can undo a visibility change

Location: `media.go:98–126`.

A cold lookup reads public metadata, an editor commits a move into a locked
list and updates the cache, then the first lookup writes its older public value
over that cache entry. All subsequent media requests trust that public entry.
This can persist until another metadata write or a restart.

Reproduction: pause a cache-miss lookup after its storage read; complete the
visibility update; resume the lookup. Both stores retain the locked list in
metadata while anonymous media still returns **200**. This is a synchronization
bug even if individual map accesses are race-detector clean.

Required correction: synchronize cache fills with policy changes, or use a
generation/version check that prevents older reads replacing newer decisions.

### S3. Sliding sessions can recreate revoked tokens

Location: `authn.go:109–128`, `store/sqlite.go:430–436`, FS `CreateSession`.

Validation reads a session and active user, then refreshes expiry through an
upsert. If logout or password reset deletes the session in between, the refresh
inserts it again. A copied old token can remain usable after revocation.

Reproduction: pause validation before its refresh, revoke all the user's
sessions, then resume. The old cookie authenticates again on both backends.

Required correction: make refresh an update of an existing, unrevoked session;
coordinate it with revocation, preferably with a session/user generation.
Do not use creation/upsert semantics for refresh.

### S4. LDAP login can inherit a same-named local administrator

Location: `authn.go:263–283`.

LDAP provisioning finds users by username alone and deliberately adopts
same-named local rows. It replaces their password/source but keeps their role.
A directory identity whose username collides with a local administrator gets
that administrator's privileges without knowing the local password. This needs
LDAP/both mode and a matching directory account; it is not an anonymous bypass.

Reproduction: authenticate a directory identity named like the seeded local
administrator, using a different directory password. The result has source
`ldap` and role `admin`.

Required correction: distinguish identities by authentication source and stable
directory identifier. Any linking/migration of local accounts must be explicit;
new directory identities must start with the viewer role.

### S5. A list-loading error can turn a locked edit into a public video

Location: `lists.go:280`, `manage.go:36`, `web/templates/edit.html:13`.

`accessListsForForm` logs an error and returns an empty list. The edit page still
renders successfully, showing only the default Public option. Saving an ordinary
title change then sends an empty `access_list`, which the backend accepts as a
request to publish the video.

Reproduction: fail `ListAccessLists` on a locked video's edit page. The page
returns 200; saving its default selection returns 303 and clears `AccessList`.

Required correction: refuse to render/save the visibility control when its data
cannot load. Preserve the current policy and require an explicit valid change.

## Medium priority: availability, performance and UX

### A1. LDAP outage also blocks valid local logins in `both` mode

Location: `authn.go:205–212`.

Fallback occurs only for `errInvalidCredentials`. Dial, TLS and service-bind
failures return before local authentication, so even a valid local emergency
administrator cannot sign in during a directory outage. Reproduced with a
directory infrastructure error and correct local credentials. Prefer routing
known local identities to local verification, with a documented outage policy.

### P1. View bookkeeping exceeds its declared memory cap

Location: `views.go:22`, `views.go:44–50`.

After 8,192 entries, cleanup removes only entries older than 24 hours. Fresh
entries are never evicted, so the map can keep growing. Each further new entry
also scans the full map while holding the mutex. The focused probe retained
8,193 recent entries despite the declared cap; the issue continues beyond that.
Enforce a real size limit and bounded eviction work.

### P2. Library/search/readiness repeatedly load every record

Location: `server.go:268`, `server.go:320`, `store/sqlite.go:202`,
`ffmpeg.go:claimJob` and `/readyz`.

Listing loads and sorts all metadata, then application code filters it. There
is no pagination in the HTML or JSON library. Readiness and worker polling also
read the complete set. Session validation writes on each authenticated page/API
request and on locked-media requests using an editor session; SQLite uses one
connection. These are scaling concerns, not measured production saturation.

Measured on this Windows machine, three sequential in-process library renders
per size, with synthetic public SQLite metadata and no browser/network load:

| Videos | Uncompressed HTML | Mean handler time |
| --- | ---: | ---: |
| 100 | 157,811 bytes | 1.81 ms |
| 1,000 | 1,536,614 bytes | 9.43 ms |

The server remains quick in this small local check, but HTML/DOM transfer grows
with the entire library. Add bounded pages, query-side filtering/counts and
lightweight readiness/queue operations. Coalesce session refreshes. No load-test
capacity or mobile rendering performance is claimed.

### U1. Most administrator deletions have no confirmation

Location: `web/static/app.js:7`, `web/templates/admin.html:80,147`.

`querySelector` attaches a submit handler only to the first matching form.
Administrator pages can contain several user/list delete forms. A script probe
with two forms observed submit-handler counts `[1, 0]`. The sole confirmation
also talks about deleting a video even on user/list forms. Bind all forms or
delegate submissions, and use action-specific confirmation text.

### U2. Copying a locked video's link can produce an unusable link

Location: `web/static/app.js:31–35`, `web/templates/watch.html`.

An administrator can open a locked video from the main library, or a visitor
can open it using an unlock cookie. In either case the watch URL can have no
key. Copy Link blindly copies that URL; a fresh recipient gets 404. This is
verified from the URL-building and access-check paths, not a browser clipboard
test. Make the distinction visible and share an available capability link, or
explain that the existing locked-list link is needed. Do not publish the video
as a workaround.

### S6. Private API/library and secret-creation pages lack cache policy

Location: `server.go:handleAPIVideo/handleAPIVideos/handleList`, admin renderers.

Unlike locked watch/media, API metadata and the main library can return locked
titles/details without `Cache-Control: private, no-store` or an authorization
`Vary` policy. The focused checks observed empty Cache-Control and Vary on these
private responses. Administrator pages that display new plaintext keys also lack
a no-store policy. This is a missing defense against browser/shared-cache
retention; a deployed proxy cache leak was **not** reproduced. Apply a consistent
no-store policy to personalized/secret responses and validate it at the proxy.

## Smaller UX and documentation issues

- The search form does not preserve `sort=views`, so a new search resets Popular
  to Recent even though category links preserve the sort selection.
- Edit fields use character-based HTML `maxlength`, while the backend enforces
  byte counts. For example, a 300-character Greek title fits the form limit but
  exceeds 512 UTF-8 bytes; the edit handler returns a bare error instead of a
  prefilled form with a field error. Upload JS already checks UTF-8 sizes.
- Session storage expiry slides, but the browser cookie's 24-hour Max-Age is
  only set at issuance. An active user can therefore be logged out after 24
  hours despite recent activity; the advertised sliding-session behavior is
  incomplete on the browser side.
- `docs/SECURITY.md` still says there is no private-video mode/all originals are
  public, and contains both an earlier successful vulnerability-scan statement
  and a later blocked-scan statement. Version these historical results and
  document the current locked-list contract and unresolved findings clearly.
- Light-first styling, escaped templates, labels/focus styling and mobile
  breakpoints are present. A new visual/responsive pass was **not available**:
  browser inventory was empty and creating an in-app browser tab failed with
  `Browser is not available: iab`. No screenshot/device compatibility claim is
  made from source inspection alone.

## Verification and limits

- Existing suite: **198 passing tests/subtests, zero failures or skips**, fresh
  `go test -count=1 -json ./...`, Go 1.27.2 Windows/amd64.
- Additional temporary probes reproduced S1–S5, A1, the view-map cap violation,
  and missing cache headers. Cache/session concurrency probes cover both stores.
  A separate scale probe produced the measurements above; the script harness
  reproduced U1. These diagnostic tests observe defects; they are not fixes.
- `go vet ./...`, `go build ./...`, `go mod verify`, JS syntax checks and
  formatting of the temporary Go probe passed.
- Isolated HTTP server with temporary SQLite/video storage: session login 200,
  authenticated upload 201, upload page 200, anonymous JSON upload 401,
  foreign-Origin edit 403 and original Range 206 with the requested 16 bytes.
  This review did not deploy or modify the user's running instance/data.
- `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` failed fetching
  `https://vuln.go.dev/index/modules.json.gz` with a socket permission error.
  **This scan is unavailable, not a clean result.** Module checksum verification
  does not substitute for it. See the [official Go vulnerability workflow](https://go.dev/doc/security/vuln/).
- Race detection unavailable: CGO is disabled and neither gcc nor clang was
  found on PATH. The deterministic concurrency probes are not a race-detector
  pass. No new Linux runtime, Nginx/TLS deployment or load-test validation.

Suggested order: S1/S2/S5 (content privacy), S3/S4 (identity/revocation), then
A1/U1/U2 and bounded-memory/pagination/cache-policy work. Each correction needs
regression tests for its failure/interleaving, followed by the full suite and
an actual browser/proxy check where applicable.
