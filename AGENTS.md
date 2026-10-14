# Working on Tiny Tube

## Scope and design

Tiny Tube is a small Go video-sharing pilot. Preserve the filesystem-only design
and standard-library-only Go implementation unless the task calls for changing
them. Viewing is public; uploads use a single Basic Auth account. Do not introduce
a database, framework, account system, or deployment dependency as incidental work.

Read `README.md` and `docs/REVIEW.md` before changing behavior. The review records
known defects, not features to preserve. Fix relevant issues when authorized, and
update the documentation when an issue is resolved.

## Code map

- `main.go`: startup, MIME registration, and graceful shutdown.
- `ui.go`: embedded web assets, template loading, and display helpers.
- `web/templates/`: shared layout and library/watch/upload pages.
- `web/static/`: shared responsive styles, theme/share behavior, player, and upload.
- `config.go`: environment settings and startup validation.
- `server.go`: application state, routes, list/watch handlers, and readiness checks.
- `upload.go`: request limits, media header checks, unique directories, and cleanup.
- `auth.go`: upload Basic Auth and environment defaults.
- `ffmpeg.go`: FFmpeg discovery, persisted queue, bounded workers, and HLS publication.
- `store/store.go`: metadata model, ID validation, JSON persistence, and search.
- `nginx.sample.conf`: optional reverse-proxy example, not a validated deployment.
- `videos/smoke/`: tracked playback sample; preserve it during normal development.
- `*_test.go` and `store/store_test.go`: regression and HTTP/FFmpeg integration tests.

## Implementation guidance

- Support Windows development and Linux deployment. Use `filepath` for filesystem
  paths and URL paths for browser links. Keep instructions valid for their named
  shell; PowerShell uses `$env:NAME`, not `set NAME=value`.
- Keep environment defaults, HTTP routes, storage layout, and player behavior
  documented accurately. The application does not automatically load `.env` files.
- Keep light mode as the default. The optional dark mode is a browser-local choice.
  Preserve accessible labels/focus states and mobile layouts. No frontend framework
  or package install is required; assets are embedded in the executable.
- Keep the interface appropriate for workplace training, presentations, and recorded
  meetings: restrained colors, compact headings, and clear task-oriented copy.
  Avoid playful illustrations, decorative numbers, and storytelling slogans.
- Treat titles, descriptions, categories, filenames, and request IDs as untrusted
  input. Keep `html/template` escaping and validate IDs before storage access.
- Preserve HTTP Range support and correct HLS MIME types when changing serving.
- Handle filesystem and metadata errors. Avoid marking failed writes or failed
  conversion as successful. Metadata reads can overlap background updates.
- For upload changes, distinguish multipart memory limits from request size limits,
  reserve a unique directory, and clean up incomplete writes and temporary files.
- For FFmpeg changes, use the same executable for discovery and execution. Account
  for conversion failure, concurrency, and application restart behavior.
- Originals and metadata are public under the current design. Changes to privacy
  must cover both Go static serving and the optional Nginx route.
- Run one server process per video directory. Metadata and job claiming are
  synchronized within an application instance; there is no cross-process lock.
- Do not commit passwords, real user uploads, executables, or generated diagnostics.
  Use a temporary video directory for checks that modify storage.

## Verification

Run the checks appropriate to the change:

```sh
go test ./...
go vet ./...
go build ./...
```

Keep focused regression coverage for substantive behavior fixes, especially upload
collisions/limits, metadata persistence, authentication, and conversion failure.
The real FFmpeg integration test skips when FFmpeg is absent. Documentation-only
changes do not require new tests. Use the race detector where the toolchain supports it.

For HTTP or playback changes, also check the affected paths manually: list/search,
watch, Basic Auth on both upload routes, multipart upload, HLS status transition,
and `206 Partial Content` for an original-file Range request. Use a small video
and temporary storage. When touching conversion, verify both FFmpeg-present and
FFmpeg-absent behavior. Report unavailable checks honestly.

Keep changes scoped to the user's request, and distinguish fixes actually made
from issues only documented in the review.
