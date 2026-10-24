# Post-merge security recheck — 2026-10-14

## Current follow-up — 2026-10-24

The [project review](PROJECT-REVIEW-2026-10-24.md) records fixes and regression
coverage for its findings. Media authorization fails closed on missing/unreadable
metadata, including orphan files. Cache fills and visibility writes are synchronized.
Session refresh updates an existing token without inserting it; writes and browser
cookie renewals are coalesced to five-minute intervals. LDAP cannot adopt local
identities/roles. Local accounts in `both` mode do not depend on LDAP availability.
Login bookkeeping updates only its timestamp.

Failed list-picker loads block upload/edit forms. Omitted API-edit visibility
preserves its value. Pages, APIs, secret-creation responses and media carry no-store
policies so they cannot be reused after authorization changes. Locked lists protect
originals, HLS, posters, pages and metadata; public videos remain public. Shared-list
links are bearer credentials, not per-person access.

Historical results below refer to earlier snapshots. In this follow-up module
checksums verify, but the pinned `govulncheck` run was blocked by a socket permission
error fetching `vuln.go.dev/index/modules.json.gz`. **The current vulnerability scan
is unavailable, not clean.** Production TLS, Nginx, Linux runtime and race detection
still need their respective environments.

The follow-up passes **228 Go tests/subtests** and **3 Node UI tests**. Isolated
HTTP checks cover locked media/key rotation, real HLS/JPEG, Range/206, session
login/logout, CSRF and explicit versus omitted edit visibility. Existing LDAP
role grants are preserved; review accounts auto-adopted by older versions.

This review covers the merged interface, JSON API, authentication, uploads,
editing/deletion, filesystem/SQLite persistence, conversion and Nginx example.
Viewing and metadata/API reads remain public by design. It is not a certification
of a production deployment or an assurance that unknown vulnerabilities do not exist.

## Session login, roles and LDAP — 2026-10-15

HTTP Basic Auth is removed. Upload/edit require an editor or administrator
session; deletion and `/admin` require an administrator; viewing and metadata
reads stay public. Sessions are 32-byte opaque tokens in an HttpOnly
SameSite=Lax cookie (`Secure` when `COOKIE_SECURE=true`); only the SHA-256 is
persisted and expiry slides 24 hours on each validated request. Local passwords
use bcrypt; inactive users, missing rows and expired sessions resolve to
anonymous, and login errors stay vague. LDAP (`AUTH_MODE=ldap|both`, go-ldap)
verifies identity only and provisions viewer rows on first login — roles are
granted locally at `/admin`, which also blocks self-demotion, last-admin
removal, and revokes sessions on password reset, deactivation and deletion.
The login rate limit still runs before any credential or session check, and
cross-origin mutations are still rejected by `CrossOriginProtection`. FS user
and session records live under the dot-leading `.auth/` directory, which video
listing skips and media serving rejects. The historical 2026-10-15
`govulncheck` v1.9.0 run reported no
vulnerabilities in called code; one advisory (GO-2026-5932, `x/crypto`) touches
only an uncalled package.

## Confirmed findings and corrections

| Finding | Correction and evidence |
| --- | --- |
| Session cookies authorizing requests from a foreign website | Go's `CrossOriginProtection` rejects cross-origin mutations with 403. Tests first reproduced accepted edits/deletion, then verify that upload/edit/delete are blocked for both stores. JSON errors and legitimate browser/CLI clients are covered. |
| A database hard link renamed as a video was publicly downloadable | The pre-fix test retrieved 4096 bytes with HTTP 200. Media now compares opened file identities with the configured DB/WAL/SHM/journal and returns 404 for aliases. All four private file types are tested. |
| Plain file serving followed links outside the storage tree | `os.Root` constrains opens to the media tree. Tests verify an external Windows junction is rejected while its target remains unchanged; the test uses a symlink on Unix. Paths with hidden components, backslashes, NTFS stream syntax, and directory listings are rejected. |
| A direct Nginx alias bypassed Go's private-file checks | The sample proxies media through Go, preserving Range and MIME behavior. Host (including port) is preserved for origin checks, and incoming forwarding headers are replaced at the proxy. Nginx itself was not available for `nginx -t`. |
| Unlimited login guesses and client bookkeeping | Login and protected routes enforce a per-socket-IP fixed-window limit before checking credentials or sessions. Valid sessions and forged forwarding headers cannot bypass a cooldown. Client memory is capped at 4096 entries with a bounded shared overflow bucket. Public reads are unaffected. |
| Missing browser execution/embedding/MIME protections | CSP permits local embedded scripts including the vendored player, with no unsafe-inline/eval exemptions. `nosniff`, frame denial, same-origin referrers and same-origin form actions apply to pages, assets, APIs and errors. Theme startup moved into an embedded external script. |
| Stalled request bodies and conversions could run indefinitely | Header/body/write deadlines are configured. Authenticated uploads get a separate total deadline; the slow-socket test gets 408 with no files left behind. Conversion deadline failures retain originals, clean staging and are not requeued endlessly. |
| Media input could select unrelated FFmpeg demuxers/protocols | Server-generated original names are validated, the MOV/Matroska demuxer is explicit and only the file protocol is allowed. Invalid names never invoke FFmpeg. Normal MP4/HLS/poster conversion still passes with real FFmpeg. |
| The local Go 1.27.1 build missed newly released security patches | `go.mod` selects Go 1.27.2. Startup rejects unpatched/unverified builds; patched 1.26.9 is also accepted. The system Go installation was left intact. See [Go's release history](https://go.dev/doc/devel/release#go1.27.2). |
| A Scoop FFmpeg launcher could hide the actual process from cancellation | Known launcher metadata is resolved to the real executable before execution. Unsupported arguments, invalid paths and cyclic chains fail discovery. Generic wrappers are not a supported cancellation guarantee. |

Other checks preserve the existing defenses: bcrypt password comparison,
no default password, loopback binding, random exclusive upload directories,
whole-request size limits, multipart/original cleanup, parameterized SQL, escaped
HTML, metadata error handling, bounded workers and edit/delete synchronization.
Stored malicious HTML and SQL strings are tested as data with both stores.

## Verification

- `go test ./... -count=1 -json`: **121 passing tests/subtests, zero skipped or
  failed**, Windows/amd64, Go 1.27.2. Both stores, browser redirects and JSON API
  behavior are covered. Real FFmpeg upload/HLS/JPEG and original Range/206 pass.
  After the session-login change (2026-10-15): **145 passing, zero failed/skipped**.
- `go mod verify`: all cached module checksums verify. This checks integrity,
  not whether a dependency contains a vulnerability.
- `go vet ./...`, `go build ./...`, and a Linux/amd64 cross-build pass with Go
  1.27.2. Linux was compiled, not executed. JavaScript syntax checks pass.
- Manual HTTP checks pass for auth, CSRF/headers, SQLite JSON upload/edit/delete,
  HLS/JPEG, Range/206 and DB privacy. Existing temporary preview records were
  preserved; only the newly created disposable videos were deleted.
- Browser checks confirm original and native HLS playback at ready state 4,
  theme persistence under CSP, the upload page and no reported JavaScript errors.
  The theme was restored to light and the agent's temporary tab was closed.
- Known private-key/token pattern checks report no findings in tracked files;
  this is a limited pattern scan, not exhaustive secret detection or a history scan.
  Generated logs/disposable data are outside tracked uploads.
- `govulncheck` v1.9.0 was attempted, but `vuln.go.dev` access failed with a socket
  permission error. **The vulnerability scan is unavailable, not clean.** Run it
  with database access before approving a production release:

  ```sh
  go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
  ```

## Deployment requirements and remaining limits

- Use HTTPS for any network-accessible login. Configure/validate real Nginx
  certificates and firewall rules on the target host. The example is not deployed
  or validated here; Linux runtime and load testing are not covered.
- Keep storage and executable/configuration files writable only by trusted service
  operators. Run FFmpeg with minimal OS permissions and keep it patched. Go's
  protocol/demuxer restriction and timeout are not an OS sandbox or a CPU/memory
  quota. Arbitrary local writers are outside the application's trust boundary.
- Go rate limiting sees the actual socket peer. Behind a proxy, users share that
  peer's bucket: tune the aggregate setting and enforce client limits at the trusted
  edge. Forwarding headers do not grant bypasses. Limits are per process.
- Public originals and metadata remain public. Locked-list videos require a list
  key, unlock cookie or editor/administrator credential on every surface. Share
  capability links over a safe channel. There is no per-person private-video mode,
  storage quota or automatic backup.
- Run one process per storage directory. Abrupt termination can leave orphan files;
  no general orphan recovery is included. Filesystem/database failures during
  deletion can leave an incomplete deletion and require operator attention.
- The pinned hls.js 1.6.19 file is vendored under `web/static` and served from
  `/assets`; playback needs no external host and CSP allows only `'self'` scripts.
  The vendored bytes were verified identical on two independent CDNs (jsDelivr,
  unpkg) and self-identify as 1.6.19. Native HLS and original-file fallback
  remain available.
- The race detector needs an unavailable C compiler in this Windows environment.
  Focused concurrency tests do run; this is not a race-detector pass.

## Primary references

- [Go CrossOriginProtection](https://pkg.go.dev/net/http#CrossOriginProtection)
- [Go traversal-resistant file APIs](https://go.dev/blog/osroot)
- [Go vulnerability management](https://go.dev/doc/security/vuln/)
- [SQLite vulnerability applicability](https://www.sqlite.org/cves.html)
- [hls.js security advisories](https://github.com/video-dev/hls.js/security)
- [FFmpeg security](https://ffmpeg.org/security.html)

The sources explain mechanisms and advisory scope; they do not replace a successful
scan of the exact build or validation of the deployment.
