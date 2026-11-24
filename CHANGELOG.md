# Changelog

## v2.2.0

### Operations

- Optional Prometheus endpoint on a separate listener (`METRICS_ADDR`):
  request counts, latency, response bytes, conversion outcomes and active
  jobs, upload bytes, counted views, throttled logins, backup health, video
  counts, storage usage, free disk space, store failures and runtime gauges.
  No new dependencies; labels never carry URLs.

[Changes since v2.1.1](https://github.com/spapas/tiny-tube/compare/v2.1.1...v2.2.0)

## v2.1.1

- Dependency updates: `go-i18n` 2.7.0, `modernc.org/sqlite` 1.63.0, `x/crypto` 0.59.0, `x/text` 0.44.0.
- CI action pins refreshed, including `actions/setup-go` v7.1.0. No application code changes.

[Changes since v2.1.0](https://github.com/spapas/tiny-tube/compare/v2.1.0...v2.1.1)

## v2.1.0

- Sticky header with grouped controls, larger touch targets and `rel=prev/next` pagination.
- Library cards use 16:9 covers with hover lift, two-line titles, scrollable filter and sort rows, and a visible result count on mobile.
- Watch page with scrollable speedbar, sticky sidebar, truncated series navigation and storyboard tooltips; share menu closes on outside click or Escape, `/` focuses search.
- Forms, admin tables and login layout refinements with sticky table headers and improved focus states.

[Changes since v2.0.1](https://github.com/spapas/tiny-tube/compare/v2.0.1...v2.1.0)

## v2.0.1

- Administration conversions page with queued, active and failed videos, per-video error messages and inline retry links.

[Changes since v2.0.0](https://github.com/spapas/tiny-tube/compare/v2.0.0...v2.0.1)

## v2.0.0

### Operations

- Operator audit log at `/admin/audit` covering uploads, edits, deletions,
  conversions, subtitles, logins, users, API keys and locked lists.
- Configurable on-disk quota (`VIDEO_DIR_QUOTA_BYTES`) with 413 rejections
  and usage display in Administration.
- Scheduled hot backups (`BACKUP_DIR`, `BACKUP_INTERVAL_HOURS`, `BACKUP_KEEP`)
  with SQLite snapshots, retention and restore documentation.

### Playback

- Adaptive HLS: 480p/720p/1080p renditions behind a measured-bandwidth master
  playlist with automatic quality selection.
- MP4/MOV originals are remuxed with `faststart` during conversion.
- Seek-preview storyboard sheets with a WebVTT index and a clickable watch-page strip.

### Library

- Multi-word search: every word must match, in any order and field, on both backends.
- Ordered video series at `/playlist` with watch-page previous/next navigation.
- Per-day view statistics for editors on the watch page.

### Upload

- Resumable chunked uploads (`POST /api/uploads` init/chunk/complete) with
  progress, retries and reload recovery; single-shot multipart stays as fallback.

[Changes since v1.2.1](https://github.com/spapas/tiny-tube/compare/v1.2.1...v2.0.0)

## v1.2.1

- Show the application version in the page footer.

[Changes since v1.2.0](https://github.com/spapas/tiny-tube/compare/v1.2.0...v1.2.1)

## v1.2.0

- Fix subtitle uploads and replacements on Windows when rooted rename returns an invalid handle.
- Serialize locked-list removal with video assignment and omit share keys from sample Nginx access logs.
- Build with Go 1.27.4 and require the November Go security patches at startup.
- Retry failed conversions from the watch page or generate HLS for existing original-only videos.
- Encode new HLS streams as H.264 4:2:0 for browser compatibility.
- Keep workers from reclaiming conversions already requeued during shutdown.
- Show free disk space and conversion counts in Administration.
- Configure the initial interface language with `DEFAULT_LANGUAGE`.
- Add a Linux systemd example and backup, restore, upgrade and smoke-check instructions.

[Changes since v1.1.0](https://github.com/spapas/tiny-tube/compare/v1.1.0...v1.2.0)

## v1.1.0

### Playback

- Share a link that opens a video at the current playback position. For locked videos, the link retains the valid list key used to open the page.
- Add clickable chapters by starting description lines with `MM:SS` or `H:MM:SS` followed by a title.
- Resume unfinished videos in the same browser or start over. Timestamp links take precedence, and finishing a video clears its saved position.
- Upload, replace or remove a WebVTT subtitle file per language from the edit page. Viewers can select subtitles with HLS or original-file playback. Subtitles follow the video's access rules.

### Interface and documentation

- Added German, Spanish, French, Italian and Chinese interface translations alongside Greek and English.
- Updated the language menu and made Greek the initial interface language. The browser remembers the viewer's choice.
- Expanded the installation guide, added screenshots and included the MIT license.

[Changes since v1.0.3](https://github.com/spapas/tiny-tube/compare/v1.0.3...v1.1.0)

## v1.0.3

- Added natural title sorting to the library, locked lists and API, so Lesson 2 sorts before Lesson 10.

## v1.0.2

- Switched to extensible go-i18n catalogs with English as the source and fallback. New languages can be added through a catalog file, with locale-aware numbers and plural forms.

## v1.0.1

- Added Greek and English interface localization and a language selector that remembers the viewer's choice.

## v1.0

- Published verified server and CLI archives for Linux, Windows and macOS on amd64 and arm64, with SHA-256 checksums.
