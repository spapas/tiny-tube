# Working on Tiny Tube

## Scope and design

Tiny Tube is a small Go video-sharing pilot. Media stays on the filesystem;
metadata defaults to SQLite via `modernc.org/sqlite` (pure Go, no CGO), with
`STORE=fs` retaining the legacy JSON backend. Preserve this single-binary design.
Viewing is public; upload/edit/delete use session login with local or LDAP
users and admin/editor/viewer roles. Do not
introduce another database, framework, account system, or dependency incidentally.

Read `README.md` and `docs/REVIEW.md` before changing behavior. The review records
known defects, not features to preserve. Fix relevant issues when authorized, and
update the documentation when an issue is resolved.

## Code map

- `main.go`: startup, MIME registration, and graceful shutdown.
- `ui.go`: embedded web assets, template loading, and display helpers.
- `web/templates/`: shared layout and library/watch/upload/edit pages.
- `web/static/`: shared responsive styles, theme/share behavior, player, and upload.
- `config.go`: environment settings and startup validation.
- `dotenv.go`: startup `.env` loading; explicitly set environment variables win.
- `server.go`: application state, routes, list/watch handlers, and readiness checks.
- `upload.go`: request limits, media header checks, unique directories, and cleanup.
- `manage.go`: authenticated metadata edits and deletion, including active-job cancellation.
- `auth.go`: environment defaults. `authn.go`: bcrypt passwords, sliding
  sessions, login/logout (HTML/JSON), role middleware and first-admin seeding.
  `ldap.go`: LDAP identity verification with local JIT provisioning.
  `admin.go`: administrator dashboard and user/role management.
- `security.go`: CSRF/CSP, bounded auth throttling, server deadlines and Go patch checks.
- `media.go`: traversal-resistant media serving and private DB file identity checks.
- `ffmpeg.go`: FFmpeg discovery, persisted queue, bounded workers, HLS/poster publication.
- `store/store.go`: metadata model, ID validation, JSON persistence, and search.
- `store/sqlite.go`: SQLite persistence, schema migration, and idempotent JSON import.
- `nginx.sample.conf`: optional reverse-proxy example, not a validated deployment.
- `testdata/sample.mp4`: tracked test fixture; runtime `videos/` is ignored.
- `dev.bat`: Windows development watcher; rebuilds embedded assets as well as Go code.
- `*_test.go` and `store/store_test.go`: regression and HTTP/FFmpeg integration tests.

## Implementation guidance

- Support Windows development and Linux deployment. Use `filepath` for filesystem
  paths and URL paths for browser links. Keep instructions valid for their named
  shell; PowerShell uses `$env:NAME`, not `set NAME=value`.
- Keep environment defaults, HTTP routes, storage layout, and player behavior
  documented accurately. `.env` is loaded from the working directory at startup;
  existing environment values, including explicitly empty ones, win over it.
  `ADDR` overrides `HOST`/`PORT`; the default is `127.0.0.1:8081`.
  `ADMIN_PASS` is required on first boot (empty user table) and cannot be blank
  or `changeme`.
- Keep light mode as the default. The optional dark mode is a browser-local choice.
  Preserve accessible labels/focus states and mobile layouts. No frontend framework
  or package install is required; assets are embedded in the executable.
- Keep the interface appropriate for workplace training, presentations, and recorded
  meetings: restrained colors, compact headings, and clear task-oriented copy.
  Avoid playful illustrations, decorative numbers, and storytelling slogans.
- Treat titles, descriptions, categories, filenames, and request IDs as untrusted
  input. Keep `html/template` escaping and validate IDs before storage access.
- Preserve HTTP Range support and correct HLS MIME types when changing serving.
- Keep cross-origin mutations blocked for HTML and JSON clients. Preserve JSON
  errors without bypassing authentication or CSRF. Rate limits must run before
  credential validation and ignore untrusted forwarding headers.
- Keep CSP free of unsafe inline/eval exceptions. Theme startup is an embedded
  external script; test uploads/player behavior when changing CSP.
- Handle filesystem and metadata errors. Avoid marking failed writes or failed
  conversion as successful. Metadata reads can overlap background updates.
- For upload changes, distinguish multipart memory limits from request size limits,
  reserve a unique directory, and clean up incomplete writes and temporary files.
- For FFmpeg changes, use the same executable for discovery and execution. Account
  for conversion failure, concurrency, and application restart behavior.
- Conversion completion must preserve edits made during processing. Cancel and
  await active conversion before deleting files; never recreate deleted metadata.
  Poster extraction is optional and shares the bounded worker. Failed posters
  must not turn a successful HLS conversion into a failed video.
- Keep SQLite `Error`/`Poster` fields round-trippable and migrate existing schemas
  without losing rows. Startup JSON import must not overwrite SQLite edits.
  Both stores must agree on missing-record errors, Unicode search and date ordering.
- Keep library/API pages bounded and apply visibility before counts or pagination.
  SQLite browse queries must retain Go Unicode folding and nanosecond/timezone date
  ordering. Legacy FS may scan JSON but must bound returned pages.
- Session renewal must update a live token, never upsert a revoked one. Coalesce
  refresh writes and renew the browser cookie too. LDAP must not adopt local
  identities/roles. Local accounts in `both` mode must survive directory outages.
  Login bookkeeping may only update its own fields.
- Synchronize media policy cache fills with metadata changes. Missing/unreadable
  metadata and failed visibility-picker loads fail closed. Omitted edit visibility
  preserves the current policy; explicit empty visibility means public.
- Public originals and metadata are public under the current design; videos
  assigned to a locked list (`store.Meta.AccessList`, `store.AccessList`)
  additionally require the list key or an editor/administrator session on every
  surface (library, watch, JSON API, `/videos/` media, posters/HLS). Changes to privacy
  must cover both Go static serving and the optional Nginx route.
  Database files, journals and staging files must not be served as media. Place
  custom `DB_PATH` outside `VIDEO_DIR` when using the optional Nginx example.
- Proxy media through Go in the Nginx sample so private file/link checks are not
  bypassed. `os.Root` prevents symlink/junction escapes; compare file identities
  to reject database hard links regardless of the URL's extension.
- Build with patched Go (1.26.9 or 1.27.2+); `go.mod` selects 1.27.2. Preserve
  startup rejection of unpatched builds. Use the real FFmpeg executable; known
  Scoop launchers are resolved to their target for reliable cancellation.
- Run one server process per video directory. Metadata and job claiming are
  synchronized within an application instance; there is no cross-process lock.
- Do not commit passwords, real user uploads, executables, or generated diagnostics.
  Use a temporary video directory for checks that modify storage.

## Verification

Run the checks appropriate to the change (always, not optionally):

```sh
gofmt -l <changed .go files>
go test ./...
go vet ./...
go build ./...
node --test tests/ui.test.cjs
```

Changed Go files must be `gofmt`-clean before finishing; run `gofmt -w` on
them when needed. Leave pre-existing formatting issues in untouched files
alone so the diff stays scoped.

Keep focused regression coverage for substantive behavior fixes, especially upload
collisions/limits, metadata persistence, authentication, and conversion failure.
The real FFmpeg integration test skips when FFmpeg is absent. Documentation-only
changes do not require new tests. Use the race detector where the toolchain supports it.

For HTTP or playback changes, also check the affected paths manually: list/search,
watch, session login on upload/edit/delete/admin routes, multipart upload, HLS/poster transition,
and `206 Partial Content` for an original-file Range request. Use a small video
and temporary storage. When touching conversion, verify both FFmpeg-present and
FFmpeg-absent behavior. Report unavailable checks honestly.

Keep changes scoped to the user's request, and distinguish fixes actually made
from issues only documented in the review.

For security work, also read `docs/SECURITY.md`, verify module checksums and run
`go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` with database access.
Do not describe an unavailable vulnerability scan as a clean result.
