#!/bin/bash

set -e

# update-go-release-version <Go version string (without a "v")>
# This script updates all configuration files in the repository that reference the Go version that
# will be used to compile releases.

VERSION=$1
if [ -z "${VERSION}" ]; then
  echo must specify Go version
  exit 1
fi

cd $(dirname $0)/..

ldrelease_config_file=.ldrelease/config.yml
circleci_config_file=.circleci/config.yml
dockerfile_for_tests=Dockerfile

function ensure_file_was_changed() {
  filename=$1
  if (( $(diff ${filename} ${filename}.tmp | grep '^>' | wc -l) != 1 )); then
    echo "failed to update Go version in ${filename}; sed expression did not match any lines or matched more than one line"
    for f in ${ldrelease_config_file} ${circleci_config_file} ${dockerfile_for_tests}; do
      rm -r ${f}.tmp
    done
    exit 1
  fi
}

sed <${ldrelease_config_file} >${ldrelease_config_file}.tmp \
  -e "/image:/s#cimg/go:[^ ]*#cimg/go:${VERSION}#"
ensure_file_was_changed ${ldrelease_config_file}

sed <${circleci_config_file} >${circleci_config_file}.tmp \
  -e "/go-release-version:/,/default:/s#default: *\"[^\"]*\"#default: ${VERSION}#"
ensure_file_was_changed ${circleci_config_file}

sed <${dockerfile_for_tests} >${dockerfile_for_tests}.tmp \
  -e "s#FROM *golang:[^-]#FROM golang:${VERSION}#"
ensure_file_was_changed ${dockerfile_for_tests}

for f in ${ldrelease_config_file} ${circleci_config_file}; do
  mv ${f}.tmp ${f}
  echo "updated ${f}"
done

echo

$(dirname $0)/verify-release-versions.sh
