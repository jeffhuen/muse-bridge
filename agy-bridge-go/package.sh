#!/usr/bin/env bash
# Build static agy-bridge-go binaries for every platform.
# Usage: ./package.sh [version]   (default version: dev)
# Output: dist/agy-bridge-go-<os>-<arch>[.exe]
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${1:-$(git rev-parse --short HEAD 2>/dev/null || echo dev)}"
OUT="dist"
rm -rf "${OUT}"
mkdir -p "${OUT}"

TARGETS="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64"

for target in ${TARGETS}; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  name="agy-bridge-go-${GOOS}-${GOARCH}"
  [ "${GOOS}" = "windows" ] && name="${name}.exe"
  CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o "${OUT}/${name}" ./cmd/agy-bridge-go
  echo "built ${OUT}/${name}"
done

ls -lh "${OUT}"
