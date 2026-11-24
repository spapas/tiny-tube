# Operating Tiny Tube

## Backup and restore

Use a stopped copy of the whole installation's data. SQLite and the media
files need to describe the same point in time. Stop Tiny Tube gracefully
(Ctrl+C in the foreground or `sudo systemctl stop tiny-tube`), wait for
shutdown to finish, and keep operator CLI writes paused until the copy ends.

1. Copy the entire `VIDEO_DIR`, including any hidden files or directories, to
   a new backup directory outside the storage tree. This retains originals,
   subtitles, generated HLS and posters, plus any legacy FS records.
2. If SQLite uses a `DB_PATH` outside `VIDEO_DIR`, copy it separately along
   with any accompanying `-wal` file. Keep those files together. Back up the
   working directory's `.env` and any external environment or service overrides.
   Record the Tiny Tube release and the configured paths. Store the backup
   privately since it contains user data, hashes and possibly credentials.
3. Restart the original service after the copy succeeds. Keep an independent
   copy on another disk or backup destination.

For example, with default paths, run these from the stopped installation's
working directory. Choose a new backup destination for each run. If you
configure Tiny Tube solely through environment variables, copy those settings
in place of the `.env` shown here.

```sh
backup=/mnt/backup/tiny-tube-2026-11-13
umask 077
mkdir -m 700 -- "$backup" &&
  cp -a -- videos "$backup/videos" &&
  cp -- .env "$backup/.env"
```

```powershell
$backup = 'E:\Backups\tiny-tube-2026-11-13'
New-Item -ItemType Directory -Path $backup -ErrorAction Stop | Out-Null
Copy-Item -LiteralPath .\videos -Destination (Join-Path $backup 'videos') -Recurse -Force -ErrorAction Stop
Copy-Item -LiteralPath .\.env -Destination $backup -Force -ErrorAction Stop
```

To rehearse a restore, copy the backup to a fresh directory and start the
matching release there. Set `VIDEO_DIR` and any custom `DB_PATH` to the
restored copies, keep `STORE` as recorded, and bind to a spare loopback port
such as `127.0.0.1:8082`. When rehearsing over plain HTTP on loopback, set
`COOKIE_SECURE=false` for the rehearsal process so the browser can sign in.
Restore the production cookie setting for HTTPS deployments. Don't point a
rehearsal instance at the live files.

- Check `/readyz`, sign in with an existing account, and confirm its role.
- Play a public video, seek in the original, and enable a subtitle track.
- Open a locked video in a fresh browser session. Verify it is unavailable
  until you open its saved list link, then verify playback and subtitles.
  Unlock cookies from the old process need to be granted again. Save any
  original list links you need separately. The database contains key hashes.
- In a recovery, stop the old service before switching its data paths to
  the verified restore. Keep the old data available until the switch is checked.

### Automatic backups

Set `BACKUP_DIR` to let the server back itself up while running. Every
`BACKUP_INTERVAL_HOURS` hours (a minute after startup for the first run) it
writes `BACKUP_DIR/tiny-tube-backup-<timestamp>/` with a copy of `VIDEO_DIR`,
a transactionally consistent SQLite snapshot (`VACUUM INTO`, so no shutdown
is needed) and a `manifest.json` recording the release, store backend and
database location. The newest `BACKUP_KEEP` runs are retained; older ones are
pruned. The backup holds no `.env`: keep the production `.env` (or the
equivalent environment) alongside the backup destination, as with stopped
backups.

Conversions in progress may leave partial staging files in a hot backup;
restoring one requeues the interrupted jobs on startup like any other
restart. To restore, stop the service, copy the newest backup's contents back
over `VIDEO_DIR` (and the external `DB_PATH` when configured), then start and
run the same readiness, login, playback and list checks as a rehearsal.

## Prometheus metrics

Set `METRICS_ADDR` to serve `GET /metrics` on its own listener:

```sh
METRICS_ADDR=127.0.0.1:9090
```

The endpoint has no authentication. Bind loopback or firewall the port, and
scrape from the host or through an SSH tunnel; it is deliberately not part of
the public mux, so neither the main port nor the Nginx sample exposes it.

```yaml
scrape_configs:
  - job_name: tiny-tube
    static_configs:
      - targets: ["127.0.0.1:9090"]
```

| Series | Meaning |
| --- | --- |
| `tiny_tube_http_requests_total{route,method,code}` | Requests served, including rejected and unmatched ones. |
| `tiny_tube_http_request_duration_seconds{route}` | Request latency histogram. |
| `tiny_tube_http_response_bytes_total{route}` | Response body bytes written. |
| `tiny_tube_auth_throttled_total` | Protected requests rejected by the per-IP limiter. |
| `tiny_tube_transcode_jobs_total{result}` | Finished conversions: `ready`, `failed`, `canceled`, `discarded`. |
| `tiny_tube_transcode_jobs_active` | Conversions running right now. |
| `tiny_tube_upload_bytes_total` | Accepted upload payload bytes. |
| `tiny_tube_views_total` | Views counted after the per-viewer debounce. |
| `tiny_tube_backup_failures_total` | Failed automatic backup runs. |
| `tiny_tube_backup_last_success_timestamp_seconds` | Unix time of the last successful automatic backup. |
| `tiny_tube_videos{status}` | Stored videos by conversion status. |
| `tiny_tube_storage_bytes` | Bytes used under `VIDEO_DIR`. |
| `tiny_tube_disk_free_bytes` | Free bytes on the `VIDEO_DIR` filesystem. |
| `tiny_tube_store_errors_total{op}` | Metadata store failures: `put`, `delete`, `views`, `list`. |
| `tiny_tube_goroutines`, `tiny_tube_heap_bytes{kind}` | Live goroutines and Go heap (`alloc`, `sys`). |
| `tiny_tube_start_time_seconds`, `tiny_tube_build_info{version}` | Process start time and running release. |

`route` is a ServeMux pattern such as `GET /watch`, never a request URL:
locked-list capability keys (`?key=lt-…`) and per-video HLS paths cannot reach
the time series. Request counters and conversion outcomes update immediately;
`tiny_tube_videos`, `tiny_tube_storage_bytes` and `tiny_tube_disk_free_bytes`
refresh every minute because they touch storage; runtime gauges are read at
scrape time. Use `/readyz` for restart decisions and these series for trends.

CPU/RSS and per-video series are not exported: the first needs OS-specific
code, the second would grow without bound from user content.

## Linux systemd example

Install the binary and use a dedicated service account. The unit stores
video data under `/var/lib/tiny-tube/videos` and reads `.env` from
`/etc/tiny-tube`. Run from the extracted Linux release directory:

```sh
sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin tiny-tube
sudo install -d -m 0750 -o root -g tiny-tube /etc/tiny-tube
sudo install -m 0750 -o root -g tiny-tube tiny-tube /usr/local/bin/tiny-tube
sudo install -m 0640 -o root -g tiny-tube .env.example /etc/tiny-tube/.env
sudoedit /etc/tiny-tube/.env
sudo install -m 0644 tiny-tube.service /etc/systemd/system/tiny-tube.service
sudo systemd-analyze verify /etc/systemd/system/tiny-tube.service
sudo systemctl daemon-reload
sudo systemctl enable --now tiny-tube
curl --fail http://127.0.0.1:8081/readyz
```

Set `ADMIN_PASS` before first start and keep the default loopback address
when using the Nginx example. Set `COOKIE_SECURE=true` for HTTPS deployments.
An explicit custom database directory outside the service's state directory
needs service-account write permission and a systemd `ReadWritePaths=`
override. Keep custom database files outside the video directory if deploying
with the Nginx sample. See the service log with
`sudo journalctl -u tiny-tube -n 50`.

For an upgrade, verify the release checksum, stop the service, take a backup,
and install the replacement binary with the same owner and mode. Keep the
existing `.env` and data paths. Start the service and check readiness, login,
playback and an upload. If rolling back after a schema change, use the prior
binary and its matching pre-upgrade backup.

## Release smoke check

On Windows and Linux, start the extracted release from a fresh working
directory with disposable data. Check initial sign-in, an authenticated
upload with and without FFmpeg, HLS and original playback, seeking, subtitles,
and locked-list access in a fresh browser session. In Chromium check the hls.js
path. On Safari check native HLS on an Apple device. Run `nginx -t` against
the target TLS configuration and confirm successful test requests with a
dummy query value leave only the path in the Nginx access log.
