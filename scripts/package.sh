#!/bin/sh
set -eu
# Build for the host platform; CGO SQLite requires the matching host libc.
cd "$(dirname "$0")/.."
: "${GO:=go}"
: "${VERSION:=dev}"
case "$VERSION" in *[!a-zA-Z0-9._-]*|'') echo 'invalid VERSION' >&2; exit 2;; esac
platform="$($GO env GOOS)-$($GO env GOARCH)"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT INT TERM
mkdir -p "$stage/reviewd-$VERSION/docs" dist
"$GO" build -trimpath -o "$stage/reviewd-$VERSION/reviewd" ./cmd/reviewd
cp README.md LICENSE go.mod go.sum "$stage/reviewd-$VERSION/"
cp docs/dependencies.md docs/specification.html docs/STATUS.md "$stage/reviewd-$VERSION/docs/"
"$GO" list -m all > "$stage/reviewd-$VERSION/docs/modules.txt"
tar -czf "dist/reviewd-$VERSION-$platform.tar.gz" -C "$stage" "reviewd-$VERSION"
sha256sum "dist/reviewd-$VERSION-$platform.tar.gz" > "dist/reviewd-$VERSION-$platform.tar.gz.sha256"
