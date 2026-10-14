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
# Windows: Produktname, Version und Icon als Ressource in die .exe (Pflicht fuer Code-Signing
# ueber SignPath: Name und Version muessen gesetzt und je Release gleich sein).
winver="${VERSION#v}"
winfile="$(echo "$winver" | grep -oE '^[0-9]+\.[0-9]+\.[0-9]+' || echo 0.0.0).0"
trap 'rm -f rsrc_windows_*.syso' EXIT
for t in "${targets[@]}"; do
  read -r os arch name <<<"$t"
  echo "building $name"
  if [ "$os" = windows ]; then
    go run github.com/tc-hib/go-winres@v0.3.3 simply --arch "$arch" --manifest cli \
      --product-name BitMerger --file-description "BitMerger - find and merge duplicate Bitwarden entries" \
      --product-version "$winver" --file-version "$winfile" \
      --copyright "Andreas Pries, MIT License" --original-filename "$name" \
      --icon winres/icon.png --out rsrc
  fi
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "dist/$name" .
  rm -f rsrc_windows_*.syso
done
(cd dist && if command -v sha256sum >/dev/null; then sha256sum bitmerger-*; else shasum -a 256 bitmerger-*; fi > SHA256SUMS)
ls -lh dist
