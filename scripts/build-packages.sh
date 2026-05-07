#!/usr/bin/env bash
set -euo pipefail

# Build .deb and .rpm packages using nFPM.
# Requires: nfpm (https://nfpm.goreleaser.com/install/)
# Expects binaries to already exist in dist/build/ (run cross-compile.sh first with --keep-build).
#
# Usage: ./scripts/build-packages.sh [VERSION]

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$PROJECT_ROOT"

VERSION="${1:-$(sed -n 's/.*Version = "\([^"]*\)".*/\1/p' relay/version/version.go)}"
DIST_DIR="$PROJECT_ROOT/dist"

# Architectures to package (linux only)
# Format: nfpm_arch:cross_compile_label
ARCHS=(
  "amd64:amd64"
  "386:386"
  "arm64:arm64"
  "armhf:armv7"
)

# Note: nfpm arch names differ from Go arch names in some cases:
#   nfpm "armhf" = Go "arm" (v7)
#   nfpm "386" = Go "386"

echo "Building packages for ld-relay v${VERSION}..."

# Ensure the linux binaries exist
for entry in "${ARCHS[@]}"; do
  IFS=':' read -r nfpm_arch arch_label <<< "$entry"
  binary="$DIST_DIR/build/ld-relay_${VERSION}_linux_${arch_label}/ld-relay"
  if [[ ! -f "$binary" ]]; then
    echo "Error: Binary not found at $binary"
    echo "Run cross-compile.sh with --keep-build first, or rebuild the linux targets."
    exit 1
  fi
done

for entry in "${ARCHS[@]}"; do
  IFS=':' read -r nfpm_arch arch_label <<< "$entry"

  for format in deb rpm; do
    echo "  ${format} (${arch_label})..."
    NFPM_ARCH="$nfpm_arch" NFPM_ARCH_LABEL="$arch_label" VERSION="$VERSION" \
      nfpm package \
        --config "$PROJECT_ROOT/nfpm.yml" \
        --packager "$format" \
        --target "$DIST_DIR/"
  done
done

echo ""
echo "Done. Packages in $DIST_DIR:"
ls -lh "$DIST_DIR"/*.deb "$DIST_DIR"/*.rpm 2>/dev/null || echo "  (none found)"
