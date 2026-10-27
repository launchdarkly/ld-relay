#!/bin/bash

# verify-release-versions.sh  (no parameters)
# This script checks all of the configuration files where a Go version is
# mentioned in the context of producing releases, and makes sure they are consistent with each other.

set -e

cd $(dirname $0)/..

github_config_file=.github/variables/go-versions.env
dockerfile_for_tests=Dockerfile

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

echo "Checking availability of Docker images..."
for docker_image in \
  "golang:${GITHUB_GO_VERSION}-bookworm"
do
  echo -n "  ${docker_image}... "
  docker pull ${docker_image} >/dev/null 2>/dev/null || { echo; echo "not available!"; exit 1; }
  echo "available"
done
