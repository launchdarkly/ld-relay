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

ldrelease_config_file=.ldrelease/config.yml
github_config_file=.github/variables/go-versions.env
dockerfile_for_tests=Dockerfile

function ensure_file_was_changed() {
  filename=$1
  if (( $(diff ${filename} ${filename}.tmp | grep '^>' | wc -l) < 1 )); then
    echo "failed to update Go version in ${filename}; sed expression did not match any lines or matched more than one line"
    diff ${filename} ${filename}.tmp || true
    for f in ${ldrelease_config_file} ${github_config_file} ${dockerfile_for_tests}; do
      rm -r ${f}.tmp
    done
    exit 1
  fi
}

sed <${ldrelease_config_file} >${ldrelease_config_file}.tmp \
  -e "/image:/s#cimg/go:[^ ]*#cimg/go:${LATEST_VERSION}#"
ensure_file_was_changed ${ldrelease_config_file}

sed <${github_config_file} >${github_config_file}.tmp \
  -e "s#latest=[^ ]*#latest=${LATEST_VERSION}#g" \
  -e "s#penultimate=[^ ]*#penultimate=${PENULTIMATE_VERSION}#g"
ensure_file_was_changed ${github_config_file}

sed <${dockerfile_for_tests} >${dockerfile_for_tests}.tmp \
  -e "s#FROM *golang:[^-]*#FROM golang:${LATEST_VERSION}#"
ensure_file_was_changed ${dockerfile_for_tests}

for f in ${ldrelease_config_file} ${github_config_file} ${dockerfile_for_tests}; do
  mv ${f}.tmp ${f}
  echo "updated ${f}"
done

echo

$(dirname $0)/verify-release-versions.sh
