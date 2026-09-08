#!/usr/bin/env bash
# Build static muse-bridge-go binaries for every platform Muse supports.
# Usage: ./package.sh [version]   (default version: dev)
# Output: dist/muse-bridge-go-<os>-<arch>[.exe]
# Pure Go stdlib + CGO_ENABLED=0 keeps each binary self-contained: no
# interpreter, no libc dependency, no installer payload beyond one file.
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${1:-dev}"
OUT="dist"
rm -rf "${OUT}"
mkdir -p "${OUT}"

# os/arch pairs covering macOS, Linux, and Windows on both arches.
TARGETS="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64"

for target in ${TARGETS}; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  name="muse-bridge-go-${GOOS}-${GOARCH}"
  [ "${GOOS}" = "windows" ] && name="${name}.exe"
  CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o "${OUT}/${name}" ./cmd/muse-bridge-go
  echo "built ${OUT}/${name}"
done

ls -la "${OUT}"
