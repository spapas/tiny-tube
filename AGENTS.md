# AGENTS.md - Tiny Tube v1

Mini YouTube clone. Go + ffmpeg + HLS + SQLite (pure Go via `modernc.org/sqlite`, no cgo). No LDAP, no Docker, single binary.

## Run / Build

```powershell
# Windows dev
$env:VIDEO_DIR="videos"; $env:PORT="8080"; $env:UPLOAD_USER="admin"; $env:UPLOAD_PASS="changeme"
go run .
# prod AlmaLinux: VIDEO_DIR=/srv/tiny-tube/videos PORT=8080 UPLOAD_USER=admin UPLOAD_PASS=xxx ./tiny-tube
```

- `go run .`, `go build -o tiny-tube .`, `go vet ./...`, `gofmt -l .`
- Dev auto-reload (cmd.exe): `dev.bat` — rebuilds+restarts on every `.go` save (polls SHA256, `tmp/` is gitignored).
- Tests: `go test ./... -count=1` (`store/store_test.go`: ValidID/Put/Get/List/filter+sort; `main_test.go`: auth/list/watch/upload ext whitelist). Stdlib only (`net/http/httptest`, `t.TempDir()`).
- Stdlib only except `modernc.org/sqlite` (pure Go, no cgo — keeps single-binary builds). Do not add other deps without asking.
- Requires `ffmpeg` in PATH (or `FFMPEG_BIN` env). Check `ffmpegAvailable()` in `ffmpeg.go`.
- Config: copy `.env.example` to `.env` (gitignored, auto-loaded at startup by `dotenv.go`); explicit env vars win over `.env`.
- Health: `GET /healthz` -> `ok`. List `/`, watch `/watch?id=`, upload page `/upload`.

## Env

| Var | Default | Notes |
|---|---|---|
| `VIDEO_DIR` | `videos` | Same dir must be shared with Nginx `alias` in prod |
| `ADDR` | `127.0.0.1:8081` | Full bind addr. Overrides `PORT` |
| `PORT` | `8081` | Compat: used as `127.0.0.1:$PORT` when `ADDR` is unset |
| `UPLOAD_USER` / `UPLOAD_PASS` | `admin` / `changeme` | BasicAuth for `GET /upload` + `POST /api/upload` only |
| `STORE` | `sqlite` | Metadata backend: `sqlite` or `fs` (legacy meta.json) |
| `DB_PATH` | `<VIDEO_DIR>/tiny-tube.db` | SQLite file (WAL mode) |
| `FFMPEG_BIN` | | Windows custom ffmpeg path fallback |

## Layout / Data

```
videos/{unix-id}/original.{mp4,mov,mkv,webm} + index.m3u8 + seg000.ts ... + poster.jpg
```

- `id` = `time.Now().Unix()` string. Must match `store.ValidID` (`^[A-Za-z0-9_-]{1,64}$`).
- `meta.json` = legacy pre-SQLite format (`store.Meta{...}`); only read once by `ImportFS`, safe to delete when `STORE=sqlite`.
- Upload flow (`main.go:handleUpload`): save original -> `Put(Status=ready|processing)` -> if ffmpeg, `go transcodeToHLS()` async -> on success `HLS=index.m3u8, Status=ready` + `Poster=poster.jpg` (frame at 1s, fallback first frame; non-fatal).
- List cards show `<img src="/videos/{id}/poster.jpg" loading="lazy">`, else gradient placeholder. `poster.jpg` served by Go `FileServer` / Nginx (needs `image/jpeg jpg` in `types`).
- `store/` is the only persistence layer. `Store` interface (`List/Get/Put/Delete/Dir`) with two backends: `SQLite{Root,db}` (default, `DB_PATH`, WAL mode; blobs stay on disk) and legacy `FS{Root}` (meta.json). `List(q)` filters case-insensitive on title+desc+category, sorts `Created` desc. On sqlite boot, `ImportFS` copies any meta.json ids missing from the DB (idempotent).

## Code map

- `main.go`: routes (`net/http` ServeMux with `GET /`, `GET /watch`, `GET /videos/`, authed upload), inline `html/template` (`listTmpl`, `watchTmpl`, `uploadTmpl`, lang=`el`), `http.FileServer` + `StripPrefix` for Go-only mode, HLS mime fix in `init()` (`.m3u8`, `.ts` needed on Windows).
- `auth.go`: `uploadAuth()` - viewers open, upload closed. `subtle.ConstantTimeCompare`, `WWW-Authenticate: Basic realm="upload"`. Keep this behavior.
- `ffmpeg.go`: single 720p rendition: `libx264 veryfast crf23 scale=-2:720 + aac 128k, hls_time 6, vod`. Logs to `log.Writer`. Never block upload on it.
- `nginx.sample.conf`: optional. Nginx serves `/videos/` directly (Range/206, cache), Go serves `/`+`/api`. Keep Go-only mode always working.

## Player / Seek

- `watchTmpl`: native HLS on Safari (`canPlayType`), `hls.js` elsewhere, fallback to `/videos/{id}/{original}` mp4.
- Seek-to-end relies on HTTP Range (206) - `FileServer` / Nginx both do it. HLS must be `ready` for best seek. If `Status=processing`, show wait message (already in template).

## Conventions / Constraints

- Stdlib only, flat files, Greek UI strings (`lang="el"`). Keep UI minimal inline CSS, no frontend build.
- Multipart limit `2 << 30` (2GB), ext whitelist `.mp4/.mov/.mkv/.webm` else force `.mp4`.
- Always `MkdirAll(videoDir/id, 0755)`.
- Do not introduce auth framework, router lib, or Docker. Keep `Go+Nginx` dual-serve contract intact.
- When changing routes/storage/ffmpeg args, update `README.md` + `nginx.sample.conf` comment if affected.
