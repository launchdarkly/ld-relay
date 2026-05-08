#!/usr/bin/env bash
set -euo pipefail

# Build and push multi-arch Docker images for ld-relay.
# Produces images for 3 variants (alpine, distroless nonroot, distroless debug) across multiple architectures,
# then creates multi-arch manifests.
#
# Usage: ./scripts/docker-build.sh [--dry-run] [VERSION]
#
# Environment variables:
#   LD_RELEASER_UPDATE_MAJOR  - "true" to also tag as vMAJOR (e.g. v8)
#   LD_RELEASER_UPDATE_LATEST - "true" to also tag as latest

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$PROJECT_ROOT"

DRY_RUN=false
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=true
  shift
fi

VERSION="${1:-$(sed -n 's/.*Version = "\([^"]*\)".*/\1/p' relay/version/version.go)}"
MAJOR_VERSION="${VERSION%%.*}"
IMAGE="launchdarkly/ld-relay"

UPDATE_MAJOR="${LD_RELEASER_UPDATE_MAJOR:-false}"
UPDATE_LATEST="${LD_RELEASER_UPDATE_LATEST:-false}"

DIST_DIR="$PROJECT_ROOT/dist"
LDFLAGS="-s -w"

# Image variant definitions: name suffix, dockerfile, architectures
declare -A VARIANT_DOCKERFILES=(
  ["alpine"]="Dockerfile.goreleaser"
  ["static-debian12-nonroot"]="Dockerfile-static-debian12-nonroot.goreleaser"
  ["static-debian12-debug-nonroot"]="Dockerfile-static-debian12-debug-nonroot.goreleaser"
)

# Alpine supports all 4 archs; distroless only supports 3 (no i386)
declare -A VARIANT_PLATFORMS=(
  ["alpine"]="linux/amd64,linux/arm64/v8,linux/arm/v7,linux/386"
  ["static-debian12-nonroot"]="linux/amd64,linux/arm64/v8,linux/arm/v7"
  ["static-debian12-debug-nonroot"]="linux/amd64,linux/arm64/v8,linux/arm/v7"
)

# Architecture label mapping for Docker tags
declare -A ARCH_LABELS=(
  ["linux/amd64"]="amd64"
  ["linux/arm64/v8"]="arm64v8"
  ["linux/arm/v7"]="armv7"
  ["linux/386"]="i386"
)

# Architecture label mapping for binary paths (matches cross-compile.sh output)
declare -A BINARY_ARCH_LABELS=(
  ["linux/amd64"]="amd64"
  ["linux/arm64/v8"]="arm64"
  ["linux/arm/v7"]="armv7"
  ["linux/386"]="386"
)

if [[ "$DRY_RUN" == "true" ]]; then
  PUSH_FLAG="--load"
else
  PUSH_FLAG="--push"
fi

# Collect manifest digests for attestation output
MANIFESTS_JSON="[]"

push_or_load() {
  if [[ "$DRY_RUN" == "true" ]]; then
    echo "    (dry-run: skipping push)"
  fi
}

build_variant() {
  local variant="$1"
  local dockerfile="${VARIANT_DOCKERFILES[$variant]}"
  local platforms="${VARIANT_PLATFORMS[$variant]}"

  # Determine tag suffix (alpine has no suffix for backwards compatibility)
  local tag_suffix=""
  if [[ "$variant" != "alpine" ]]; then
    tag_suffix="-${variant}"
  fi

  echo ""
  echo "=== Building variant: ${variant} ==="
  echo "    Dockerfile: ${dockerfile}"
  echo "    Platforms:  ${platforms}"

  # Build per-architecture images for version tag
  IFS=',' read -ra platform_list <<< "$platforms"
  for platform in "${platform_list[@]}"; do
    local arch_label="${ARCH_LABELS[$platform]}"
    local tag="${IMAGE}:${VERSION}${tag_suffix}-${arch_label}"

    local binary_arch_label="${BINARY_ARCH_LABELS[$platform]}"
    echo "  Building ${tag} (${platform})..."
    docker buildx build \
      --platform "$platform" \
      --tag "$tag" \
      --pull \
      --file "$dockerfile" \
      --build-arg "BINARY=dist/build/ld-relay_${VERSION}_linux_${binary_arch_label}/ld-relay" \
      $PUSH_FLAG \
      .
  done

  # Create version manifest
  local version_manifest="${IMAGE}:${VERSION}${tag_suffix}"
  local manifest_images=()
  for platform in "${platform_list[@]}"; do
    local arch_label="${ARCH_LABELS[$platform]}"
    manifest_images+=("${IMAGE}:${VERSION}${tag_suffix}-${arch_label}")
  done

  echo "  Creating manifest: ${version_manifest}"
  if [[ "$DRY_RUN" == "false" ]]; then
    docker manifest create "$version_manifest" "${manifest_images[@]}" --amend 2>/dev/null || \
      docker manifest create "$version_manifest" "${manifest_images[@]}"
    docker manifest push "$version_manifest"

    local digest
    digest=$(docker manifest inspect "$version_manifest" -v 2>/dev/null | jq -r '.digest // empty' 2>/dev/null || \
             docker buildx imagetools inspect "$version_manifest" --format '{{.Manifest.Digest}}' 2>/dev/null || echo "")
    if [[ -n "$digest" ]]; then
      MANIFESTS_JSON=$(echo "$MANIFESTS_JSON" | jq --arg img "$IMAGE" --arg dig "$digest" '. += [{"image": $img, "digest": $dig}]')
    fi
  fi

  # Alpine also gets an explicit "-alpine" alias
  if [[ "$variant" == "alpine" ]]; then
    local alpine_manifest="${IMAGE}:${VERSION}-alpine"
    echo "  Creating manifest: ${alpine_manifest}"
    if [[ "$DRY_RUN" == "false" ]]; then
      docker manifest create "$alpine_manifest" "${manifest_images[@]}" --amend 2>/dev/null || \
        docker manifest create "$alpine_manifest" "${manifest_images[@]}"
      docker manifest push "$alpine_manifest"
    fi
  fi

  # Major version tag (e.g. v8, v8-static-debian12-nonroot)
  if [[ "$UPDATE_MAJOR" == "true" ]]; then
    local major_images=()
    for platform in "${platform_list[@]}"; do
      local arch_label="${ARCH_LABELS[$platform]}"
      local major_tag="${IMAGE}:v${MAJOR_VERSION}${tag_suffix}-${arch_label}"
      echo "  Tagging ${major_tag}..."
      if [[ "$DRY_RUN" == "false" ]]; then
        docker buildx imagetools create --tag "$major_tag" "${IMAGE}:${VERSION}${tag_suffix}-${arch_label}"
      fi
      major_images+=("$major_tag")
    done

    local major_manifest="${IMAGE}:v${MAJOR_VERSION}${tag_suffix}"
    echo "  Creating manifest: ${major_manifest}"
    if [[ "$DRY_RUN" == "false" ]]; then
      docker manifest create "$major_manifest" "${major_images[@]}" --amend 2>/dev/null || \
        docker manifest create "$major_manifest" "${major_images[@]}"
      docker manifest push "$major_manifest"
    fi

    if [[ "$variant" == "alpine" ]]; then
      local alpine_major_manifest="${IMAGE}:v${MAJOR_VERSION}-alpine"
      echo "  Creating manifest: ${alpine_major_manifest}"
      if [[ "$DRY_RUN" == "false" ]]; then
        docker manifest create "$alpine_major_manifest" "${major_images[@]}" --amend 2>/dev/null || \
          docker manifest create "$alpine_major_manifest" "${major_images[@]}"
        docker manifest push "$alpine_major_manifest"
      fi
    fi
  fi

  # Latest tag
  if [[ "$UPDATE_LATEST" == "true" ]]; then
    local latest_images=()
    for platform in "${platform_list[@]}"; do
      local arch_label="${ARCH_LABELS[$platform]}"
      local latest_tag="${IMAGE}:latest${tag_suffix}-${arch_label}"
      echo "  Tagging ${latest_tag}..."
      if [[ "$DRY_RUN" == "false" ]]; then
        docker buildx imagetools create --tag "$latest_tag" "${IMAGE}:${VERSION}${tag_suffix}-${arch_label}"
      fi
      latest_images+=("$latest_tag")
    done

    local latest_manifest="${IMAGE}:latest${tag_suffix}"
    echo "  Creating manifest: ${latest_manifest}"
    if [[ "$DRY_RUN" == "false" ]]; then
      docker manifest create "$latest_manifest" "${latest_images[@]}" --amend 2>/dev/null || \
        docker manifest create "$latest_manifest" "${latest_images[@]}"
      docker manifest push "$latest_manifest"
    fi

    if [[ "$variant" == "alpine" ]]; then
      local alpine_latest_manifest="${IMAGE}:latest-alpine"
      echo "  Creating manifest: ${alpine_latest_manifest}"
      if [[ "$DRY_RUN" == "false" ]]; then
        docker manifest create "$alpine_latest_manifest" "${latest_images[@]}" --amend 2>/dev/null || \
          docker manifest create "$alpine_latest_manifest" "${latest_images[@]}"
        docker manifest push "$alpine_latest_manifest"
      fi
    fi
  fi
}

echo "Docker build for ld-relay v${VERSION}"
echo "  Update major (v${MAJOR_VERSION}): ${UPDATE_MAJOR}"
echo "  Update latest: ${UPDATE_LATEST}"
echo "  Dry run: ${DRY_RUN}"

for variant in alpine static-debian12-nonroot static-debian12-debug-nonroot; do
  build_variant "$variant"
done

# Output manifests JSON for attestation
echo ""
echo "=== Build complete ==="
if [[ "$DRY_RUN" == "false" ]]; then
  echo "IMAGES_AND_DIGESTS=${MANIFESTS_JSON}"
  echo "$MANIFESTS_JSON" > "$DIST_DIR/images_and_digests.json"
fi
