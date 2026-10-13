# Tiny Tube v1 (vibe, Go + HLS + SQLite)

SQLite metadata (pure Go, no cgo), video blobs on disk. No LDAP/Docker. Viewers open, upload with 1 password.

## Run Windows (dev here)
```
copy .env.example .env
go run .
```
Open http://127.0.0.1:8081 , /upload for upload.

Dev auto-reload (rebuild+restart on every .go save, cmd.exe):
```
dev.bat
```
(PORT env also works: 127.0.0.1:$PORT when ADDR is unset.)

## Run AlmaLinux (prod pilot, same binary)
```
VIDEO_DIR=/srv/tiny-tube/videos ADDR=127.0.0.1:8081 UPLOAD_USER=admin UPLOAD_PASS=xxx ./tiny-tube
# systemd unit points here. Optional Nginx: see nginx.sample.conf
```

## Layout
```
videos/{unix-id}/original.mp4 + index.m3u8 + seg000.ts ... + meta.json
```

Upload mp4 -> status=processing -> ffmpeg background -> index.m3u8 ready.
Player: native HLS on Safari, hls.js elsewhere, fallback to mp4.
Seek to end = Range request (206), no full 1GB download. Needs HLS ready for best seek.

## Dual serve
- Go only: Go serves /videos/* itself.
- Go+Nginx: Nginx serves /videos/ via alias (same dir), Go serves / + /api.
