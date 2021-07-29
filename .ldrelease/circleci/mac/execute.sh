#!/bin/bash

set -ue

# Performs a delegated release step in a CircleCI Mac container. This mechanism is described
# in scripts/circleci/README.md. All of the necessary environment variables should already be
# in the generated CircleCI configuration.

mkdir -p artifacts

export LD_RELEASE_TEMP_DIR=/tmp/project-releaser-temp
mkdir -p ${LD_RELEASE_TEMP_DIR}

STEP="$1"
SCRIPT="$2"
echo
echo "[${STEP}] executing ${SCRIPT}"
"./${SCRIPT}"
