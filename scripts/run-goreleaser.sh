#!/bin/bash

# run-goreleaser.sh <goreleaser options>...
#
# Builds the Docker image and all other executables that we intend to publish.
# This also pushes the image to DockerHub unless we have specifically told it not to with
# the --skip-publish option.

GORELEASER_VERSION=${GORELEASER_VERSION:-v0.141.0}

# Get the lines added to the most recent changelog update (minus the first 2 lines)
RELEASE_NOTES=`(GIT_EXTERNAL_DIFF='bash -c "diff --unchanged-line-format=\"\" $2 $5" || true' git log --ext-diff -1 --pretty= -p CHANGELOG.md)`

curl -sL https://git.io/goreleaser | GOPATH=`mktemp -d` VERSION=${GORELEASER_VERSION} bash -s -- \
  --rm-dist --release-notes <(echo "${RELEASE_NOTES}") $@
