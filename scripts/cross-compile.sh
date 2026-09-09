#!/usr/bin/env bash
set -euo pipefail

# Cross-compile ld-relay for all supported OS/architecture targets.
# Produces tarballs and a checksums file in the dist/ directory.
#
# Usage: ./scripts/cross-compile.sh [--keep-build] [VERSION]
#   --keep-build: Keep the intermediate build directory (needed for build-packages.sh)
#   VERSION defaults to the value in relay/version/version.go

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$PROJECT_ROOT"

KEEP_BUILD=false
if [[ "${1:-}" == "--keep-build" ]]; then
  KEEP_BUILD=true
  shift
fi

VERSION="${1:-$(sed -n 's/.*Version = "\([^"]*\)".*/\1/p' relay/version/version.go)}"
DIST_DIR="$PROJECT_ROOT/dist"
LDFLAGS="-s -w"

# OS/arch matrix (all supported targets; darwin/386 and windows/arm excluded)
TARGETS=(
  "darwin/amd64"
  "darwin/arm64"
  "linux/386"
  "linux/amd64"
  "linux/arm/7"
  "linux/arm64"
  "windows/386"
  "windows/amd64"
  "windows/arm64"
)

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

echo "Building ld-relay v${VERSION} for ${#TARGETS[@]} targets..."

for target in "${TARGETS[@]}"; do
  IFS='/' read -r goos goarch goarm <<< "$target"

  suffix=""
  arch_label="$goarch"
  if [[ "$goarch" == "arm" && -n "$goarm" ]]; then
    arch_label="armv${goarm}"
  fi

  binary_name="ld-relay"
  if [[ "$goos" == "windows" ]]; then
    binary_name="ld-relay.exe"
  fi

  archive_name="ld-relay_${VERSION}_${goos}_${arch_label}"
  build_dir="$DIST_DIR/build/${archive_name}"
  mkdir -p "$build_dir"

  echo "  ${goos}/${arch_label}..."

  GOARM="${goarm:-}" CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$build_dir/$binary_name" .

  cp README.md "$build_dir/" 2>/dev/null || true
  cp LICENSE "$build_dir/" 2>/dev/null || true

  tar -C "$DIST_DIR/build" -czf "$DIST_DIR/${archive_name}.tar.gz" "$archive_name"
done

echo "Generating checksums..."
cd "$DIST_DIR"
shasum -a 256 *.tar.gz > checksums.txt

echo "{\"version\":\"$VERSION\"}" > "$DIST_DIR/metadata.json"

if [[ "$KEEP_BUILD" == "false" ]]; then
  rm -rf "$DIST_DIR/build"
fi

echo ""
echo "Done. Artifacts in $DIST_DIR:"
ls -lh "$DIST_DIR"
