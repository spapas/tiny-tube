#!/usr/bin/env bash
set -euo pipefail

# Inputs are environment values, never interpolated into executable shell text.
if [[ ! "${RELEASE_TAG:-}" =~ ^v[0-9]+\.[0-9]+(\.[0-9]+)?$ ]]; then
  echo 'Release tags must look like v1.0 or v1.0.0.' >&2
  exit 1
fi
case "${GOOS:-}/${GOARCH:-}" in
  linux/amd64|linux/arm64|windows/amd64|windows/arm64|darwin/amd64|darwin/arm64) ;;
  *) echo 'Unsupported release platform.' >&2; exit 1 ;;
esac

export CGO_ENABLED=0
package="tiny-tube-${RELEASE_TAG}-${GOOS}-${GOARCH}"
stage="dist/staging/$package"
mkdir -p "$stage/docs/screenshots"
suffix=''
if [[ "$GOOS" == windows ]]; then suffix='.exe'; fi

go build -trimpath -ldflags='-s -w' -o "$stage/tiny-tube$suffix" .
go build -trimpath -ldflags='-s -w' -o "$stage/tiny-tube-cli$suffix" ./cmd/cli
go version -m "$stage/tiny-tube$suffix"
go version -m "$stage/tiny-tube-cli$suffix"

# Explicit allowlist: never package local .env files, databases or user uploads.
cp README.md .env.example nginx.sample.conf "$stage/"
cp docs/REVIEW.md docs/SECURITY.md docs/PROJECT-REVIEW-2026-10-24.md docs/I18N.md "$stage/docs/"
cp docs/screenshots/library.jpg docs/screenshots/watch.jpg docs/screenshots/upload.jpg "$stage/docs/screenshots/"
cp web/static/hls.js.LICENSE.txt "$stage/"
printf 'Tiny Tube %s\nCommit: %s\nPlatform: %s/%s\nToolchain: %s\n' \
  "$RELEASE_TAG" "$(git rev-parse HEAD)" "$GOOS" "$GOARCH" "$(go env GOVERSION)" \
  > "$stage/VERSION.txt"

if [[ "$GOOS" == windows ]]; then
  (cd dist/staging && zip -qr "../$package.zip" "$package")
  unzip -t "dist/$package.zip"
else
  tar -czf "dist/$package.tar.gz" -C dist/staging "$package"
  tar -tzf "dist/$package.tar.gz"
fi
