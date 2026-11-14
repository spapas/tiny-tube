# Changelog

## Unreleased

- Fix subtitle uploads and replacements on Windows when rooted rename returns an invalid handle.

- Serialize locked-list removal with video assignment and omit share keys from sample Nginx access logs.
- Build with Go 1.27.4 and require the November Go security patches at startup.
- Retry failed conversions from the watch page or generate HLS for existing original-only videos.
- Encode new HLS streams as H.264 4:2:0 for browser compatibility.
- Keep workers from reclaiming conversions already requeued during shutdown.
- Show free disk space and conversion counts in Administration.
- Configure the initial interface language with `DEFAULT_LANGUAGE`.
- Add a Linux systemd example and backup, restore, upgrade and smoke-check instructions.

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
