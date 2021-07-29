#!/bin/bash

# Does any necessary setup to build or test Go code.
#
# If this is not a module and therefore needs to be built inside of GOPATH, then you must set
# the environment variable LD_RELEASE_GO_IMPORT_PATH to the base import path of the project
# (which may not be the same as the repo URL, if it is a private mirror or if we're using
# gopkg.in). The checkout directory is copied to a temporary location under a temporary
# GOPATH, which becomes the current directory.

GO=$(which go || true)
if [ -z "$GO" ]; then
  GO=/usr/local/go/bin/go
fi

if [ ! -f "go.mod" ]; then
  if [ -z "${LD_RELEASE_GO_IMPORT_PATH}" ]; then
    echo "Must set LD_RELEASE_GO_IMPORT_PATH to build non-module projects" >&2;
    exit 1
  fi
  export GOPATH=${LD_RELEASE_TEMP_DIR}/gopath
  TEMP_BUILD_DIR="${GOPATH}/src/${LD_RELEASE_GO_IMPORT_PATH}"
  if [ ! -d "${GOPATH}" ]; then
    mkdir -p "${GOPATH}"
    mkdir -p ${TEMP_BUILD_DIR}
    cp -r ${LD_RELEASE_PROJECT_DIR}/* "${TEMP_BUILD_DIR}"
  fi
  cd "${TEMP_BUILD_DIR}"
fi
