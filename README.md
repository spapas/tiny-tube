# Tiny Tube

A small video-sharing server written in Go, with media on disk, SQLite metadata,
and optional FFmpeg conversion to HLS with preview posters. Viewing is public;
uploading and editing require login with an editor or administrator account,
and deleting or managing users requires an administrator. Videos can also be
assigned to locked lists, visible only through a long list link
(`/view_list?key=…`); editors and administrators bypass keys for management.
Users authenticate
with a local password (bcrypt) or against LDAP; roles are assigned locally.
SQLite uses the pure Go
`modernc.org/sqlite` driver, so single-binary builds do not require CGO. Legacy
filesystem JSON metadata remains available with `STORE=fs`.

This is a small pilot. Upload isolation, request limits, metadata replacement,
and bounded conversion workers are covered by regression tests. See the
[review and remaining limitations](docs/REVIEW.md) before deployment.

## Requirements

- A patched Go release: 1.26.9 or 1.27.2 and newer. The module selects Go 1.27.2
  automatically; startup rejects builds from older/unverified toolchains.
- A writable video directory, with space for both originals and generated HLS files.
- Optional FFmpeg on `PATH`, with the `libx264` and AAC encoders, for HLS conversion.
  Without FFmpeg, the server serves the original file; playback depends on the
  browser's support for its container and codecs.
- Native HLS playback and original-file playback do not require that script.

## Run on Windows (PowerShell)

From the repository directory:

```powershell
$env:VIDEO_DIR = "videos"
$env:ADDR = "127.0.0.1:8081"
$env:ADMIN_USER = "admin"
$env:ADMIN_PASS = "replace-with-a-strong-password"
go run .
```

Open <http://localhost:8081> for the searchable list, <http://localhost:8081/upload>
to upload after login, and <http://localhost:8081/admin> to manage users.
On first boot with an empty user table the server creates the administrator
from `ADMIN_USER`/`ADMIN_PASS`; afterwards users and roles are managed at
`/admin`. For operator role fixes without the web UI, `cli.bat` (or
`go run ./cmd/cli`) offers `users list` and
`users set-role --username U --role admin|editor|viewer` against the same
`VIDEO_DIR`/`STORE`/`DB_PATH` settings; it refuses to demote the last active
administrator. Runtime storage starts empty; the small
video under `testdata/` is a test fixture.

To build a Windows executable:

```powershell
go build -o tiny-tube.exe .
.\tiny-tube.exe
```

Environment settings apply to the current shell and processes started from it.
The application also loads `.env` from its working directory at startup. Copy
`.env.example` to `.env` and set `ADMIN_PASS`; explicitly set environment variables
take precedence, including empty values. Values may be quoted; there is no variable
expansion or inline-comment support. `.env` is ignored by Git.

## Locked lists

Public videos stay visible to everyone. For sensitive material, an
administrator creates any number of locked lists at `/admin/lists` (section
«Κλειδωμένες λίστες»), each with its own long random key. Uploads and edits
offer a visibility picker: public by default, or one locked list per video.
Existing videos stay public unless moved.

- Holders of `/view_list?key=…` see exactly that list; without the key its
  videos are invisible everywhere — library, search, category counts, watch
  page, JSON API, originals, HLS segments and posters all answer 404.
- The first key visit sets an unlock cookie so posters, HLS segments and
  later pages keep working without repeating the key.
- Editors and administrators see every video without a key, so lost links
  never block management.
- A link is a bearer credential: anyone it is forwarded to gains access.
  Share links only over a safe channel. Rotating the key («Αλλαγή κλειδιού»)
  revokes every copy at once, including previously granted cookies.
  Deleting a list is refused while it still holds videos, so videos cannot
  silently become public.
- The Nginx sample proxies every route through Go, so the same checks apply
  behind the proxy.

For scripts, pass the key as `?key=…` on `/api/videos`, `/api/video`,
`/watch` and `/videos/…`, or reuse the unlock cookie from a link visit.

For automatic rebuilding/restarting on Windows, run `dev.bat` from cmd.exe or
`.\dev.bat` from PowerShell. It watches Go sources (excluding test files),
embedded HTML/CSS/JavaScript, module files and `.env`. Configure the password before starting the watcher.

## Run on Linux / AlmaLinux

Build on the target platform with Go installed:

```sh
go build -o tiny-tube .
VIDEO_DIR=/srv/tiny-tube/videos ADDR=127.0.0.1:8081 ADMIN_USER=admin \
  ADMIN_PASS='replace-with-a-strong-password' ./tiny-tube
```

For a Linux binary built from Windows:

```powershell
$env:GOOS = "linux"
$env:GOARCH = "amd64" # match the target machine's architecture
go build -o tiny-tube .
Remove-Item Env:GOOS, Env:GOARCH
```

The same source supports both platforms; a Windows executable cannot run natively
on Linux. A systemd unit is not included. If creating one, set an absolute
`VIDEO_DIR`, supply the upload credentials, and ensure the service account can
write there and find FFmpeg on its `PATH`.

## Configuration

| Variable | Default | Behavior |
| --- | --- | --- |
| `VIDEO_DIR` | `videos` | Video storage; relative paths use the process working directory. |
| `ADDR` | unset | Full `host:port` bind address, overriding `HOST` and `PORT`. Default effective address is `127.0.0.1:8081`. |
| `HOST` | `127.0.0.1` | Bind host when `ADDR` is unset. Set `0.0.0.0` explicitly for IPv4 network access. |
| `PORT` | `8081` | HTTP port when `ADDR` is unset; `PORT=8080` remains supported. |
| `STORE` | `sqlite` | Metadata backend: `sqlite` or legacy `fs`. Video files remain on disk in both modes. |
| `DB_PATH` | `<VIDEO_DIR>/tiny-tube.db` | SQLite metadata database in WAL mode. Relative paths use the working directory. |
| `ADMIN_USER` | `admin` | First administrator, created on first boot when no users exist. Ignored afterwards. |
| `ADMIN_PASS` | required on first boot | Password for the first administrator; startup rejects an unset/blank password or `changeme` when the user table is empty. |
| `AUTH_MODE` | `local` | Login source: `local`, `ldap`, or `both` (LDAP first, local fallback). |
| `COOKIE_SECURE` | `false` | Set `true` behind HTTPS so session cookies carry the Secure flag. |
| `LDAP_ADDR` | `ldap://localhost:389` | Directory address (`ldap://` or `ldaps://`). |
| `LDAP_BASE_DN` | `dc=example,dc=com` | Fallback search base and direct-bind default. |
| `LDAP_BIND_DN` | unset | Service account for subtree search; without it, users bind directly. |
| `LDAP_BIND_PASSWORD` | unset | Service account password. |
| `LDAP_SEARCH_BASES` | unset | `;`-separated search bases enabling subtree search. |
| `LDAP_USER_FILTER` | `(uid=%s)` | Search filter with the escaped username as `%s`. |
| `LDAP_USER_DN` | `uid=%s,<base>` | Direct-bind DN template for the username. |
| `LDAP_ATTR_EMAIL` | `mail` | Directory attribute mapped to the user record. |
| `LDAP_ATTR_FIRST_NAME` | `givenName` | Directory attribute for the display name. |
| `LDAP_ATTR_LAST_NAME` | `sn` | Directory attribute for the display name. |
| `LDAP_ALWAYS_UPDATE` | `true` | Refresh LDAP user records from the directory on every login. |
| `MAX_UPLOAD_BYTES` | `2147483648` | Maximum **whole multipart request**, including form fields and overhead: 2 GiB. Larger requests return HTTP 413. |
| `TRANSCODE_WORKERS` | `1` | Maximum concurrent FFmpeg processes, between 1 and 16. Pending jobs remain on disk. |
| `AUTH_REQUESTS_PER_MINUTE` | `60` | Protected-route requests per source IP per minute, checked before authentication; 1–3600. Excess requests return 429 with `Retry-After`. |
| `UPLOAD_TIMEOUT_SECONDS` | `1800` | Total authenticated multipart read deadline; 1–86400 seconds. Stalled uploads return 408 and are cleaned up. |
| `TRANSCODE_TIMEOUT_SECONDS` | `1800` | Total HLS/poster conversion deadline; 1–86400 seconds. Expired jobs become `failed`, preserving originals. |
| `FFMPEG_BIN` | unset | Explicit executable path/name, preferred over `PATH`. An invalid explicit value fails startup. Otherwise FFmpeg is discovered on `PATH`. |

Empty optional values fall back to the defaults. For direct LAN access, set `HOST`
explicitly (or set `ADDR=0.0.0.0:8081`) and configure the firewall. Session cookies
need HTTPS when used over a network; the Go server itself serves HTTP. Video files
and public metadata are public. Login sessions last 24 hours sliding; password resets,
deactivation and user deletion revoke sessions. Unlock cookies are session
cookies signed per process and die with the browser session or a restart;
reopening the list link restores them.

Browser mutations must originate from this site. Cross-origin upload/edit/delete
requests return 403, including JSON clients; command-line clients without browser
origin headers continue to work. Changing `Accept`/`format=json` does not bypass
authentication, request limits or origin protection. CSP blocks inline scripts
and embedding the interface in frames; MIME sniffing is disabled.

The auth limit uses the actual socket source address and ignores forwarding
headers. Behind Nginx, clients share its IP bucket: tune the Go aggregate limit
and apply per-client limits at the trusted edge. Public viewing/API reads remain
available during an auth cooldown. Limiter memory is bounded.

Headers have a 10-second timeout and a 64 KiB limit. Ordinary request reads are
limited to 30 seconds; authenticated uploads use their separate configured
deadline. Response writes are limited to `max(30 minutes, UPLOAD_TIMEOUT_SECONDS
+ 60 seconds)`, so unusually slow original downloads may need a larger setting.

## Upload and playback

The interface defaults to light mode. The theme button switches between light and
dark and remembers the selection in browser-local storage; it does not follow the
operating system's theme automatically. Library, watch, and upload pages adapt to
mobile, tablet, and desktop screens. Categories can be combined with text search.

The upload page supports file selection or drag-and-drop, shows the chosen file,
and reports upload progress and errors without discarding the form. Without
JavaScript, its standard multipart form still works. The watch page provides link
copying and access to the original file. The interface uses restrained colors and
task-oriented wording for workplace use. Cards display an extracted poster when
available and a neutral placeholder otherwise. The player offers speeds from
0.5× to 2× and attempts playback with sound; where the browser blocks
it, the viewer presses play. The video is never muted.

The watch page links to `/edit?id=<id>` and provides deletion with a confirmation
prompt. `GET /edit` and `POST /api/edit` require an editor or administrator login;
`POST /api/delete` and `/admin` require an administrator. The watch page shows
management actions only to users holding the matching role. Editing changes title/description/category/visibility while
preserving playback data. Deletion cancels and waits for active conversion, then
removes the original, HLS, poster and metadata. There is no undo.

Each authorized watch-page load counts one view, debounced to one per viewer
address per video every 24 hours so refreshes do not inflate totals; denied
requests never count. Totals appear on library cards and the watch page, and
the library offers an optional «Δημοφιλή» sort next to the default recency
order (`/?sort=views`, mirrored by `/api/videos?sort=views`).

1. An authenticated `POST /api/upload` accepts multipart fields `title` (required),
   `desc`, `category`, and `file`.
2. The original is saved to `VIDEO_DIR/<id>/`. Recognized filename extensions are
   `.mp4`, `.mov`, `.mkv`, and `.webm`; unsupported extensions or mismatched
   container signatures return HTTP 415. This is a basic header check, not full
   codec/file validation. Titles are limited to 512 bytes, descriptions to 8,192,
   and categories to 256. A unique directory is reserved for each upload, and
   incomplete uploads are removed if saving the original or metadata fails.
3. If FFmpeg is available, metadata starts as `queued`. A bounded worker marks it
   `processing`, generates HLS in a staging directory, and publishes it only after
   FFmpeg succeeds and produces a complete playlist. A JPEG poster is extracted
   at one second, falling back to the first frame; poster failure is non-fatal.
   Status then becomes `ready`.
   Without FFmpeg, status is immediately `ready` for original-file playback.
4. Reload the watch page after conversion to use HLS. Safari can use native HLS;
   other supported browsers use hls.js, with original-file fallback when HLS
   support is unavailable or HLS playback fails. The page uses the vendored
   hls.js 1.6.19 (`web/static/hls.min.js`, Apache-2.0, see `hls.js.LICENSE.txt`);
   no external browser access is required.
5. Conversion failures become `failed`, with a visible message and the original
   retained. Pending/interrupted jobs resume on startup if FFmpeg is available;
   otherwise they become `failed`. On graceful shutdown, active conversions are
   cancelled and requeued. Failed jobs are not automatically retried.

### REST API for upload (automation / LLM clients)

Programmatic clients log in with `POST /api/login` (`{"username", "password"}`)
and reuse the session cookie, exactly like a browser. For scripts that should
not juggle cookies, an administrator can issue a long-lived API key per user
at `/admin` (section «Κλειδιά API») or with
`cli.bat users api-key --username U --name N`, and the client sends it as
`Authorization: Bearer tt-…`. Keys carry the user's role, never expire, and
are revoked from `/admin` or with `users api-key-revoke --prefix P`; only
their SHA-256 is stored and the plaintext is shown once at creation.
Uploads stay
`multipart/form-data`; only the response format changes. Send
`Accept: application/json` (or `?format=json`) to get JSON instead of the
browser `303` redirect. Error responses use the same HTTP status codes with a
`{"error": "..."}` body. Anonymous JSON clients get `401`; logged-in users
without the role get `403`. Reading is public, like the library pages, except
locked-list videos (see above); writing
requires the matching role.

| Method | Route | Auth | Description |
| --- | --- | --- | --- |
| `POST` | `/api/login` | none | JSON `{"username", "password"}`; sets the session cookie, returns `{"username", "role"}`. |
| `POST` | `/logout` | session | Revokes the session cookie. |
| `POST` | `/api/upload` | editor | Multipart fields `title` (required), `desc`, `category`, `file`, `access_list` (empty for public, otherwise a locked-list id). JSON clients get `201` with `id`, `title`, `status`, `original`, `access_list`, `watch_url`, `video_url`, `api_url`. Browsers get `303` to `/watch?id=`. |
| `GET` | `/view_list?key=…` | none | HTML page with exactly one locked list's videos; grants an unlock cookie. Without `key`, shows everything the cookie already unlocked. Unknown keys return 404. |
| `GET` | `/api/videos?q=&category=` | none | JSON `{"total", "count", "q", "category", "sort", "items"}` with the same filtering as the library page. Locked videos are excluded unless `?key=…`, an unlock cookie, or an editor/administrator credential is presented; `total` counts only visible videos. `sort=views` orders by view count, otherwise by recency. |
| `GET` | `/api/video?id=<id>` | none | JSON metadata for one video, including `status` (`queued`/`processing`/`ready`/`failed`), `access_list` and `views`. `404` when missing or locked without access. |
| `POST` | `/api/edit` | editor | Form fields `id`, `title`, `desc`, `category`, `access_list`. JSON clients get `200` with `id`, `title`, `watch_url`, `api_url`. |
| `POST` | `/api/delete` | admin | Form field `id`. JSON clients get `200` with `{"id", "deleted": true}`. |

Poll `GET /api/video?id=<id>` until `status` is `ready` (or `failed`), then
open `watch_url`. Serve over HTTPS when credentials cross a network; the Go
server itself serves HTTP (see the Nginx example).

```sh
curl -c session.txt -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"secret"}' http://127.0.0.1:8081/api/login
curl -b session.txt -H "Accept: application/json" \
  -F title="Demo" -F desc="Recorded meeting" -F category="Meetings" \
  -F file=@demo.mp4 http://127.0.0.1:8081/api/upload
curl "http://127.0.0.1:8081/api/video?id=<id>"
```

With an API key instead of the session cookie:

```sh
curl -H "Authorization: Bearer tt-..." -H "Accept: application/json" \
  -F title="Demo" -F desc="Recorded meeting" -F category="Meetings" \
  -F file=@demo.mp4 http://127.0.0.1:8081/api/upload
```

```python
import os
import requests
base = "http://127.0.0.1:8081"
s = requests.Session()
s.post(f"{base}/api/login",
    json={"username": "admin", "password": os.environ["ADMIN_PASS"]}).raise_for_status()
with open("demo.mp4", "rb") as f:
    r = s.post(f"{base}/api/upload",
        headers={"Accept": "application/json"},
        data={"title": "Demo", "desc": "Recorded meeting", "category": "Meetings"},
        files={"file": ("demo.mp4", f, "video/mp4")})
r.raise_for_status()
video_id = r.json()["id"]
print(requests.get(f"{base}/api/video", params={"id": video_id}).json())
```

```text
videos/
  tiny-tube.db       # SQLite metadata; WAL/SHM files may also exist
  <id>/
    original.mp4     # or .mov, .mkv, .webm
    meta.json        # legacy FS mode, or retained migration source
    hls-<generated>/ # published after successful conversion
      index.m3u8
      seg000.ts
      poster.jpg     # optional preview frame
      ...
```

HLS currently has a single rendition scaled to 720 pixels high, H.264 video,
AAC audio, and a target segment duration of six seconds. It is not adaptive
multi-quality streaming. Originals are kept after conversion.

Original files support HTTP Range requests, but seeking also depends on the file
layout and browser. The code does not apply MP4 `faststart` to originals. Multipart
file parts use at most an 8 MiB memory threshold before spilling to temporary disk;
the separate request limit is enforced by `http.MaxBytesReader`.

Existing videos, including root-level `index.m3u8` and `poster.jpg`, remain compatible.
SQLite startup imports legacy `meta.json` records whose IDs are absent from the
database. Repeated imports leave existing SQLite edits untouched. JSON files are
retained; switching back to `STORE=fs` does not export later SQLite edits. Existing
SQLite databases are upgraded to retain conversion error messages and add
user/session tables without losing rows.
In FS mode, metadata is written to a temporary file, synced/closed, and replaced.
Application reads and writes use in-process synchronization. Run **one server process per video
directory**; shared storage across multiple instances is not supported.

## Optional Nginx deployment

Go can serve everything itself. [nginx.sample.conf](nginx.sample.conf) proxies all
routes, including `/videos/`, through Go so its private-file and link checks apply.
Range/206 and HLS/poster content types are preserved. It is a starting point,
not a complete deployment configuration:

- Replace the sample hostname and storage path, and configure TLS certificates;
  the configured files must exist before Nginx can start.
- The sample has a 2 GiB whole-request cap and 300-second inactivity timeouts.
  Keep the proxy cap consistent if changing `MAX_UPLOAD_BYTES`. Upload request
  buffering is disabled to avoid an additional full copy at the proxy.
- Restrict direct access to the Go port when TLS/auth routing relies on the proxy.
- Go already checks session roles. Additional Nginx upload authentication is
  optional and must not break the session cookie if enabled.
- Keep SQLite files and their journals private. The example blocks database
  extensions and staging paths; Go also checks file identities to reject DB aliases.
  Its upstream uses the default port 8081; adjust it if changing `ADDR`/`PORT`.

Capacity has not been measured. Validate the edited configuration with `nginx -t`
on the deployment machine, then check a real authenticated upload and playback.

## Development

```sh
go test ./...
go vet ./...
go build ./...
```

Tests cover concurrent uploads, request limits, cleanup, metadata replacement,
authentication, HTTP playback/Range support, bounded conversion concurrency, and
shutdown/restart recovery, both storage backends, schema/import migration, and
editing/deleting during conversion. A real FFmpeg upload-to-HLS integration test runs when
FFmpeg is on `PATH`, and is skipped otherwise.

`GET /healthz` reports liveness. `GET /readyz` checks writable storage and readable
metadata, returning 503 if either fails; it does not certify FFmpeg codecs or disk
capacity. For manual verification, check search, authenticated upload, conversion,
and browser playback. Use a temporary `VIDEO_DIR` for checks that modify storage.

Contributor guidance is in [AGENTS.md](AGENTS.md). Runtime uploads are ignored by
Git; only the fixture under `testdata/` remains tracked. Back up video files and
the SQLite database, including a custom external `DB_PATH`. Stop the server before
copying a SQLite database, or use SQLite's backup facility; copying only the live
`.db` file may omit uncheckpointed WAL changes. There is no built-in quota,
recovery or backup mechanism.

HTML templates, styles, and scripts live under `web/` and are embedded in the Go
executable. No frontend package installation or separate asset deployment is
needed. Rebuild/restart the server after editing these files.

The post-merge security check and its deployment limitations are recorded in
[docs/SECURITY.md](docs/SECURITY.md). Keep video/database storage writable only by
the service account, run FFmpeg with minimal OS privileges, use HTTPS for network
access, and rebuild existing binaries with the selected patched toolchain.
