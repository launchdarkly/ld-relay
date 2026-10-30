#!/usr/bin/env bash
set -euo pipefail

# Orchestrates the full release pipeline: cross-compile, package, and Docker build.
#
# Usage: ./scripts/release.sh [--dry-run] [VERSION]
#   --dry-run: Build artifacts locally without pushing Docker images or uploading releases.
#   VERSION defaults to the value in relay/version/version.go.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

DRY_RUN=""
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN="--dry-run"
  shift
fi

VERSION="${1:-}"

"$SCRIPT_DIR/cross-compile.sh" --keep-build ${VERSION:+"$VERSION"}
"$SCRIPT_DIR/build-packages.sh" ${VERSION:+"$VERSION"}
"$SCRIPT_DIR/docker-build.sh" $DRY_RUN ${VERSION:+"$VERSION"}
