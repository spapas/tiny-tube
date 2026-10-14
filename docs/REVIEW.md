# Project review and fixes — 2026-10-14

The pilot keeps its filesystem-only design, standard-library Go implementation,
public viewing, and single-account upload authentication. The initial findings
have now been addressed in code, with regression tests.

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
| Floating player version and silent HLS failure | hls.js is pinned to 1.6.19. Fatal HLS/native video errors fall back to the original; original playback errors are shown. The CDN dependency remains. |
| Hidden list/watch/rendering failures | Storage errors return 500 and are logged; templates are buffered before sending. `/healthz` is liveness; `/readyz` checks storage/metadata and returns 503 on failure. |

HLS output is generated in a staging directory and then published under a unique
`hls-<generated>/` directory. Metadata references the published playlist only after
successful conversion. Existing root-level playlists remain compatible.

Other fixes: unknown routes now return 404, unsupported extensions and invalid
container signatures return 415, and Nginx's sample now includes upload caps,
inactivity timeouts, active certificate directives, and no unverified capacity
claim. The README documents the current behavior and compatibility changes.

## Verification

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
- hls.js is still served from an external CDN. Offline deployments should vendor
  that pinned release and its license. A local download attempt encountered a
  socket access restriction, so no vendored asset is included.
- Failed jobs are not automatically retried. Pending jobs resume at startup.
  Abrupt process termination can leave staging/output directories or uploads
  without metadata; there is no general orphan cleanup or deletion interface.
- FFmpeg cancellation targets the resolved executable. Prefer the real executable
  over a shell/launcher wrapper that may spawn independent child processes.
- All media and metadata are public. There are no viewer accounts, private videos,
  storage quotas, automated backups, or rate limits.
- The Nginx example requires actual certificate files and deployment-specific paths.
  Nginx validation, Linux runtime execution, and load testing have not been performed
  here. Capacity remains unmeasured. Browser checks used the local preview server.
- The race detector was not run: the local Go environment has CGO disabled and no
  C compiler was found on `PATH`. Concurrency regression tests do run.
- The existing sample description contains replacement characters. Its intended
  original text is unknown and has been preserved.

## Primary references

The pinned player version and error API were checked against the
[hls.js project](https://github.com/video-dev/hls.js) and its
[releases](https://github.com/video-dev/hls.js/releases).
Proxy settings follow the Nginx
[core module](https://nginx.org/en/docs/http/ngx_http_core_module.html) and
[proxy module](https://nginx.org/en/docs/http/ngx_http_proxy_module.html)
documentation. Local Go documentation/source was used for multipart limits and
request cleanup.
