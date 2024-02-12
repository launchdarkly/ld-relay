#!/bin/bash

# verify-release-versions.sh  (no parameters)
# This script checks all of the configuration files where a Go version and/or Alpine version is
# mentioned in the context of producing releases, and makes sure they are consistent with each other.

set -e

cd $(dirname $0)/..

github_config_file=.github/variables/go-versions.env
dockerfile_for_tests=Dockerfile
dockerfile_for_releases=Dockerfile.goreleaser

function fail_for_file() {
  echo "failed to parse $1 version from $2"
  exit 1
}

GITHUB_GO_VERSION=$(sed <${github_config_file} -n 's/^latest=\(.*\)$/\1/p')
if [ -z "${GITHUB_GO_VERSION}" ]; then
  fail_for_file Go ${github_config_file}
fi
echo "${github_config_file} (for CI tests) is using Go ${GITHUB_GO_VERSION}"

DOCKERFILE_TESTS_GO_VERSION=$(sed <${dockerfile_for_tests} -n 's/FROM *golang:\([^-]*\)-.*/\1/p')
if [ -z "${DOCKERFILE_TESTS_GO_VERSION}" ]; then
  fail_for_file ${dockerfile_for_tests}
fi
echo "${dockerfile_for_tests} (for images in CI tests) is using Go ${DOCKERFILE_TESTS_GO_VERSION}"

if [[ "${GITHUB_GO_VERSION}" != "${DOCKERFILE_TESTS_GO_VERSION}" ]]; then
  echo; echo "Go versions are out of sync!"
  exit 1
fi

echo "Go versions are in sync"

DOCKERFILE_TESTS_ALPINE_VERSION=$(sed <${dockerfile_for_tests} -n 's/FROM alpine:\(.*\).*/\1/p')
if [ -z "${DOCKERFILE_TESTS_ALPINE_VERSION}" ]; then
  fail_for_file Alpine ${dockerfile_for_tests}
fi
DOCKERFILE_TESTS_ALPINE_MINOR_VERSION=$(sed <${dockerfile_for_tests} -n 's/FROM *golang:.*alpine\([^ ]*\).*/\1/p')
if [ -z "${DOCKERFILE_TESTS_ALPINE_MINOR_VERSION}" ]; then
  fail_for_file "Alpine minor" ${dockerfile_for_tests}
fi
echo "${dockerfile_for_tests} (for images in CI tests) is using Alpine ${DOCKERFILE_TESTS_ALPINE_VERSION} (and minor version ${DOCKERFILE_TESTS_ALPINE_MINOR_VERSION})"

DOCKERFILE_RELEASES_ALPINE_VERSION=$(sed <${dockerfile_for_releases} -n 's/FROM *alpine:\([^ ]*\).*/\1/p')
if [ -z "${DOCKERFILE_RELEASES_ALPINE_VERSION}" ]; then
  fail_for_file Alpine ${dockerfile_for_releases}
fi
echo "${dockerfile_for_releases} (for building releases) is using Alpine ${DOCKERFILE_RELEASES_ALPINE_VERSION}"

if [[ "${DOCKERFILE_TESTS_ALPINE_VERSION}" != "${DOCKERFILE_RELEASES_ALPINE_VERSION}" ]]; then
  echo; echo "Alpine versions are out of sync!"
  exit 1
fi

if [[ "${DOCKERFILE_TESTS_ALPINE_MINOR_VERSION}" != "${DOCKERFILE_TESTS_ALPINE_VERSION%.*}" ]]; then
  echo; echo "Alpine minor version is out of sync!"
  exit 1
fi

echo "Alpine versions are in sync"

echo "Checking availability of Docker images..."
for docker_image in \
  "alpine:${DOCKERFILE_RELEASES_ALPINE_VERSION}" \
  "golang:${GITHUB_GO_VERSION}-alpine${DOCKERFILE_TESTS_ALPINE_MINOR_VERSION}"
do
  echo -n "  ${docker_image}... "
  docker pull ${docker_image} >/dev/null 2>/dev/null || { echo; echo "not available!"; exit 1; }
  echo "available"
done
