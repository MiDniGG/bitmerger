#!/usr/bin/env bash
# Builds all release binaries into dist/ and writes SHA256SUMS.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
rm -rf dist && mkdir -p dist
targets=(
  "darwin arm64 bitmerger-macos-apple-silicon"
  "darwin amd64 bitmerger-macos-intel"
  "linux amd64 bitmerger-linux-x64"
  "linux arm64 bitmerger-linux-arm64"
  "windows amd64 bitmerger-windows-x64.exe"
)
for t in "${targets[@]}"; do
  read -r os arch name <<<"$t"
  echo "building $name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "dist/$name" .
done
(cd dist && if command -v sha256sum >/dev/null; then sha256sum bitmerger-*; else shasum -a 256 bitmerger-*; fi > SHA256SUMS)
ls -lh dist
