# Tiny Tube v1 (vibe, Go only + HLS)

Filesystem only, no DB/LDAP/Docker. Viewers open, upload with 1 password.

## Run Windows (dev here)
```
set VIDEO_DIR=videos
set PORT=8080
set UPLOAD_USER=admin
set UPLOAD_PASS=changeme
go run .
```
Open http://localhost:8080 , /upload for upload.

## Run AlmaLinux (prod pilot, same binary)
```
VIDEO_DIR=/srv/tiny-tube/videos PORT=8080 UPLOAD_USER=admin UPLOAD_PASS=xxx ./tiny-tube
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
