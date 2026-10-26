# Project review and fixes — 2026-10-14

## English source catalogs and extensible i18n — 2026-10-26

The first localization implementation below was replaced with standard go-i18n
JSON catalogs keyed by stable English identifiers. English is now the source and
fallback language. `go-i18n` handles named variables and CLDR plural rules;
`golang.org/x/text` negotiates language tags and formats numbers. All embedded
locale files are discovered automatically, including their native `language.name`.
Adding a translation requires only its JSON file and a rebuild, with no changes
to server/browser language lists, the selector, or date-formatting code. Partial
translations fall back to English. The browser preserves the source locale of
fallback messages so its CLDR plurals match the server, including cases such as
Russian 21 falling back to English "21 views".

Catalog validation rejects unknown IDs, malformed files and incompatible named
variables. Tests add German, Russian and Arabic catalogs in memory to verify
registration, negotiation, native selector names, formatting, fallback and all six
plural categories. User content and the JSON API's English errors stay intact.
See [I18N.md](I18N.md) for the translator workflow.

Verification on Windows/amd64 with Go 1.27.3: Go tests (including real FFmpeg),
vet, build, changed-file formatting, module checksum/tidy checks and all seven
Node UI tests pass. A temporary SQLite instance was checked for both languages,
selector persistence, login, protected forms, multipart upload, HLS/poster
publication, Range/206 and browser playback. The local race detector remains
unavailable with CGO disabled; concurrent-language tests run without it.

## Greek and English interface — 2026-10-25

All public, login, editing and administrator pages now share embedded Greek and
English message catalogs. The header language form works without JavaScript,
remembers the choice for a year and preserves local page/query context. Browser
language negotiation applies until a choice is saved, with Greek as fallback.
User content, stored metadata, API identifiers/errors and CLI/log messages retain
their existing representation. Server templates use separate immutable function
maps per language so concurrent requests cannot change one another's translations.
Upload/player/theme/share/confirmation messages use the same catalogs through an
external script; CSP requires no new exceptions. Dates, sizes and counts are
localized, and the development watcher rebuilds on translation changes.
Regression tests cover page coverage, catalog/placeholder completeness,
cookie preference, redirects, same-origin protection and concurrent rendering.

Verification on Windows/amd64 with Go 1.27.3: Go tests (including real FFmpeg),
vet, build, scoped formatting and the six Node UI tests pass. Manual checks with
temporary SQLite storage cover both languages, login/protected routes, multipart
upload, HLS/poster publication, original Range/206, editing and browser playback.
The local race detector is unavailable because CGO is disabled and no C compiler
was found on PATH; the concurrent-language regression runs without it.

## Continuous integration — 2026-10-24

Version tags (`v1.0`, `v1.0.1`, etc.) now gate release packaging on the same full
CI checks. Linux/Windows/macOS amd64 and arm64 archives contain the server, operator
CLI, example configuration and documentation; all six builds must succeed before
publication with SHA-256 checksums. Go/UI/SQLite are included; FFmpeg is separate.
Packages are unsigned and cross-built; native runtime coverage remains Linux/Windows
amd64 rather than every release platform. Only the publication job can write
repository contents. Branch pushes and pull requests cannot publish releases.

The hosted setup now reads the exact `toolchain` directive explicitly and verifies
`go env GOVERSION`; relying on `go-version-file` selected the older language
minimum in the first run. The module now selects Go 1.27.3 and startup rejects
builds older than Go 1.26.10/1.27.3, covering the October 19 x509 security patch.

GitHub Actions now defines Linux/Windows tests with installed FFmpeg, Linux race
detection, coverage reports, builds without CGO, scoped formatting checks, vet,
module integrity/tidy checks, Node UI tests and live vulnerability scans for both
platforms. Run results are linked from the README badge. Earlier statements about
unavailable local race detection/scans describe their original local environment;
only successful CI jobs establish the corresponding hosted-runner results.

## Additional fixes — 2026-10-24

The [new review](PROJECT-REVIEW-2026-10-24.md) has been followed by fixes for
all five high-priority findings: fail-closed media authorization, synchronized
visibility caching, revocation-safe session refresh, local/LDAP identity isolation,
and fail-closed visibility forms. Additional changes cover local-login availability
during LDAP outages, private cache policies, bounded view bookkeeping, bounded
HTML/API pages, SQLite filtering and lightweight readiness/queue reads.
Every administrator deletion receives a matching confirmation, locked-video sharing
requires a usable capability link, and Greek edit errors preserve the form.
Historical checks below retain their original versions/dates.

The pilot keeps media on disk and public viewing. Session login with local or
LDAP users and admin/editor/viewer roles replaced the former single-account
Basic Auth on 2026-10-15; see [SECURITY.md](SECURITY.md).
The merged implementation supports SQLite metadata (pure Go, no CGO) by default
and legacy filesystem JSON via `STORE=fs`. The initial findings have been addressed
in code, with regression tests.

The subsequent [security recheck](SECURITY.md) found and fixed additional issues:
CSRF on mutations, database aliases, unrestricted auth attempts, missing browser
headers, slow-request/conversion deadlines and an outdated Go build toolchain.
It passes 121 tests/subtests on Go 1.27.2. The newer JSON API is included; the
historical merge verification below remains a record of that earlier snapshot.

## Follow-up performance and correctness fixes — 2026-10-19

- Metadata writes no longer revert a concurrent view increment: `Put` preserves
  the stored `views` value and `IncrementViews` owns the column, in both
  backends (regression test `TestPutPreservesConcurrentViews`).
- Media serving no longer exposes `.json` files; only real media extensions are
  public, so a legacy `meta.json` is not downloadable (`publicMediaPath`).
- `/videos/` reuses one `os.Root` and a prebuilt file server instead of opening
  the storage root per request, and a small in-process cache avoids a metadata
  lookup for every HLS segment, poster and Range request. Metadata writes
  (`putMeta`/`deleteMeta`) invalidate the cache; missing rows stay uncached.
- Unlock grants resolve list prefixes with a single metadata read instead of one
  lookup per unlocked prefix, and API-key `last_used_at` is written at most once
  every five minutes instead of on every authenticated request.
- The admin overview no longer reads the metadata list twice.

## Merge integration

The professional responsive light/dark interface, upload validation, random IDs,
bounded conversion queue and restart recovery are combined with SQLite, `.env`,
`ADDR`, posters, playback speeds/autoplay, editing/deleting, and Windows dev tooling.
Runtime smoke files were removed by the incoming branch; the original small MP4
is retained under `testdata/sample.mp4` for reproducible tests.

Integration corrections covered by regression tests:

- SQLite preserves `Poster` and conversion `Error`, upgrades older schemas,
  returns the same missing-record errors as FS, and matches Unicode search and
  chronological ordering across time zones.
- Startup JSON import is idempotent and does not overwrite SQLite edits.
- Conversion completion preserves edits made during processing. Deletion cancels
  active conversion and waits before removing files, preventing resurrection.
- Upload/edit/delete remain authenticated. Blank/`changeme` passwords remain
  rejected; `.env` does not override existing environment values.
- Database/journal files and staging paths are blocked by Go media serving; the
  Nginx sample protects database extensions and staging paths. Custom database
  paths should be outside the media directory when using Nginx.
- Posters run within the worker limit and are optional. Existing root-level HLS
  and poster paths remain compatible. Embedded assets are watched by `dev.bat`.

## Findings addressed

| Initial finding | Result |
| --- | --- |
| Uploads in the same second could overwrite each other | Cryptographically random IDs, exclusive directory/file creation, and a test preserving 20 simultaneous uploads. |
| Multipart memory threshold mistaken for an upload cap | `http.MaxBytesReader` enforces `MAX_UPLOAD_BYTES` over the whole request. The file-part memory threshold is 8 MiB; oversize requests return 413. |
| Ignored storage errors and incomplete uploads | Startup checks writable storage; uploads check creation/copy/sync/close/metadata errors and remove incomplete originals and multipart temporary files. |
| Direct writes could expose truncated metadata | Metadata is written to a same-directory temporary file, synced/closed, then replaced. Application reads/writes use a mutex. Concurrent replacement is tested on Windows. |
| Known default upload credentials | Startup rejects an unset/blank password or `changeme`. The default bind address is now loopback; network access requires an explicit `HOST`. HTTPS is still the deployment's responsibility. |
| `FFMPEG_BIN` checked but not executed | An explicit override is resolved first and used by the actual conversion command. Invalid overrides fail startup; absent optional FFmpeg permits original playback. |
| Failed/interrupted conversion stuck in `processing` | Failures persist `failed` plus a visible message. Startup requeues interrupted work, or marks it failed if FFmpeg is absent. Graceful shutdown cancels/requeues active work. |
| Unbounded FFmpeg processes | A fixed number of workers claims the oldest queued jobs from metadata on disk. Default concurrency is one, configurable up to 16. |
| Floating player version and silent HLS failure | hls.js is pinned to 1.6.19 and vendored under `web/static`. Fatal HLS/native video errors fall back to the original; original playback errors are shown. No CDN dependency remains. |
| Hidden list/watch/rendering failures | Storage errors return 500 and are logged; templates are buffered before sending. `/healthz` is liveness; `/readyz` checks storage/metadata and returns 503 on failure. |

HLS output is generated in a staging directory and then published under a unique
`hls-<generated>/` directory. Metadata references the published playlist only after
successful conversion. Existing root-level playlists remain compatible.

Other fixes: unknown routes now return 404, unsupported extensions and invalid
container signatures return 415, and Nginx's sample now includes upload caps,
inactivity timeouts, active certificate directives, and no unverified capacity
claim. The README documents the current behavior and compatibility changes.

## Verification of the merged implementation

- `go test ./... -count=1 -json`: 71 passing tests/subtests, zero skipped or
  failed results on Windows/amd64 with Go 1.27.1. The real FFmpeg test also
  checks successful JPEG poster generation and serving.
- `go vet ./...`, `go build ./...` and a pure Go Linux/amd64 cross-build pass.
  Linux runtime execution and Nginx validation remain unavailable here.
- Manual HTTP checks against the merged SQLite preview pass for list/search,
  readiness, authentication on all five management/upload routes, multipart
  upload, HLS/poster publication and MIME types, original Range/206, metadata
  editing, deletion of the newly created disposable upload, and DB privacy.
  Preview storage is in the system temporary directory; repository uploads
  were not used.
- JavaScript syntax checks pass. Browser verification of the merged player and
  edit page is recorded in [UI.md](UI.md).
- The race detector still requires an unavailable C compiler; focused
  concurrency tests exercise upload, metadata, worker, edit and deletion races.

## Verification before merge

- `go test -count=1 -json ./...` passes on Windows/amd64 with Go 1.27.1:
  28 passing test/subtest results, zero skipped tests after the UI update.
- Regression tests cover upload concurrency, known-length and unknown-length
  oversized requests, invalid input, metadata failure cleanup, large multipart
  temporary-file cleanup, authentication, search, metadata replacement, corruption,
  HTTP Range responses, error responses, worker concurrency, and shutdown/restart.
- The real FFmpeg integration test uploads the sample through an HTTP test server,
  waits for conversion, and fetches the complete playlist and a nonempty segment
  with the expected MIME types. It passes locally; environments without FFmpeg
  skip this test.
- `go vet ./...` passes. Windows/amd64 and Linux/amd64 executable builds pass;
  Linux was cross-compiled, not executed. Builds used temporary output paths and
  preserved the existing local executable.
- JavaScript syntax was checked with Node. Browser checks of the updated interface
  are recorded in [UI.md](UI.md).
- Documentation links, ignore rules, formatting, and unchanged sample files were
  checked. No real user uploads were created in the repository.

## Remaining limits

- Run one process per `VIDEO_DIR`. Locks and job claiming are in-process; there is
  no distributed or cross-process coordination.
- Storage errors cannot always be persisted when storage itself is unavailable.
  Conversion-status write failures are logged; startup reconciles the previously
  persisted pending state once storage is usable.
- Container signatures are a basic check, not complete media validation. Browsers
  may not support an original's codecs. Originals are not remuxed for MP4 faststart.
- HLS is a single 720-pixel-high rendition, without adaptive quality. Reload the
  watch page after a queued/processing conversion completes.
- hls.js 1.6.19 is vendored (`web/static/hls.min.js`, Apache-2.0) and served
  from `/assets`; offline deployments work without external browser access.
  The vendored bytes were verified identical on jsDelivr and unpkg.
- Failed jobs are not automatically retried. Pending jobs resume at startup.
  Abrupt process termination can leave staging/output directories or uploads
  without metadata; there is no general orphan cleanup. Authenticated deletion
  removes known videos, not arbitrary orphan directories.
- FFmpeg cancellation targets the resolved executable. Prefer the real executable
  over a shell/launcher wrapper that may spawn independent child processes.
- All public media and metadata are public. Locked lists hide grouped videos
  behind per-list long keys (`/view_list?key=…`, invisible without the key
  across pages, API and media files); editors/administrators bypass keys for
  management. There are no per-user private videos, storage quotas or automated
  backups. Protected-route requests are rate limited;
  public traffic and overall storage capacity still require deployment controls.
- The Nginx example requires actual certificate files and deployment-specific paths.
  Nginx validation, Linux runtime execution, and load testing have not been performed
  here. Capacity remains unmeasured. Browser checks used the local preview server.
- The race detector was not run: the local Go environment has CGO disabled and no
  C compiler was found on `PATH`. Concurrency regression tests do run.

## Primary references

The pinned player version and error API were checked against the
[hls.js project](https://github.com/video-dev/hls.js) and its
[releases](https://github.com/video-dev/hls.js/releases).
Proxy settings follow the Nginx
[core module](https://nginx.org/en/docs/http/ngx_http_core_module.html) and
[proxy module](https://nginx.org/en/docs/http/ngx_http_proxy_module.html)
documentation. Local Go documentation/source was used for multipart limits and
request cleanup.
