#!/bin/bash

set -e

# update-alpine-version <Alpine version string (without a "v")>
# This script updates all configuration files in the repository that reference the Alpine version that
# will be used to build Docker images.

VERSION=$1
if [ -z "${VERSION}" ]; then
  echo must specify Alpine version
  exit 1
fi

cd $(dirname $0)/..

dockerfile_for_tests=Dockerfile
dockerfile_for_releases=Dockerfile.goreleaser

function ensure_file_was_changed() {
  filename=$1
  if (( $(diff ${filename} ${filename}.tmp | grep '^>' | wc -l) == 0 )); then
    echo "failed to update Alpine version in ${filename}; sed expression did not match any lines"
    for f in ${ldrelease_config_file} ${circleci_config_file} ${dockerfile_for_tests}; do
      rm -r ${f}.tmp
    done
    exit 1
  fi
}

# the golang: Docker images are only specific to an Alpine minor version, not a patch version
MINOR_VERSION=${VERSION%.*}
sed <${dockerfile_for_tests} >${dockerfile_for_tests}.tmp \
  -e "s#-alpine.* as builder#-alpine${MINOR_VERSION} as builder#" \
  -e "s#FROM alpine:.*#FROM alpine:${VERSION}#"
ensure_file_was_changed ${dockerfile_for_tests}

sed <${dockerfile_for_releases} >${dockerfile_for_releases}.tmp \
  -e "s#FROM *alpine:.*#\FROM alpine:${VERSION}#"
ensure_file_was_changed ${dockerfile_for_releases}

for f in ${dockerfile_for_tests} ${dockerfile_for_releases}; do
  mv ${f}.tmp ${f}
  echo "updated ${f}"
done

echo

$(dirname $0)/verify-release-versions.sh
