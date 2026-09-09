#!/bin/bash

set -e

# This script updates all configuration files in the repository that reference the Go version that
# will be used to compile releases.

LATEST_VERSION=$1
PENULTIMATE_VERSION=$2

if [ -z "${LATEST_VERSION}" ] || [ -z "${PENULTIMATE_VERSION}" ]; then
  echo "Usage: $0 <latest Go version> <penultimate Go version>"
  exit 1
fi

cd $(dirname $0)/..

github_config_file=.github/variables/go-versions.env
dockerfile_for_tests=Dockerfile

# Asserts the substituted file ended up with the version we asked for, rather than that it
# changed at all. The Dockerfile only tracks the latest version, so a release that moves only
# the penultimate version legitimately leaves it alone.
function ensure_file_has_version() {
  filename=$1
  pattern=$2
  if ! grep -qE -- "${pattern}" ${filename}.tmp; then
    echo "failed to update Go version in ${filename}; expected to match /${pattern}/ after substitution"
    diff ${filename} ${filename}.tmp || true
    for f in ${github_config_file} ${dockerfile_for_tests}; do
      rm -f ${f}.tmp
    done
    exit 1
  fi
}

# Versions are interpolated into grep patterns below, so escape the dots.
LATEST_PATTERN=${LATEST_VERSION//./\\.}
PENULTIMATE_PATTERN=${PENULTIMATE_VERSION//./\\.}

sed <${github_config_file} >${github_config_file}.tmp \
  -e "s#latest=[^ ]*#latest=${LATEST_VERSION}#g" \
  -e "s#penultimate=[^ ]*#penultimate=${PENULTIMATE_VERSION}#g"
ensure_file_has_version ${github_config_file} "^latest=${LATEST_PATTERN}$"
ensure_file_has_version ${github_config_file} "^penultimate=${PENULTIMATE_PATTERN}$"

sed <${dockerfile_for_tests} >${dockerfile_for_tests}.tmp \
  -e "s#FROM *golang:[^-]*#FROM golang:${LATEST_VERSION}#"
ensure_file_has_version ${dockerfile_for_tests} "^FROM golang:${LATEST_PATTERN}-"

for f in ${github_config_file} ${dockerfile_for_tests}; do
  mv ${f}.tmp ${f}
  echo "updated ${f}"
done

echo

$(dirname $0)/verify-release-versions.sh
