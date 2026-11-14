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
