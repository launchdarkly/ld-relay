#!/bin/bash

# run-goreleaser.sh <goreleaser options>...
#
# Builds the Docker image and all other executables that we intend to publish.
# This also pushes the image to DockerHub unless we have specifically told it not to with
# the --skip-publish option.

export GOPATH=$(mktemp -d)
export GOBIN=$GOPATH/bin
go install github.com/goreleaser/goreleaser

# Get the lines added to the most recent changelog update (minus the first 2 lines)
RELEASE_NOTES=`(GIT_EXTERNAL_DIFF='bash -c "diff --unchanged-line-format=\"\" $2 $5" || true' git log --ext-diff -1 --pretty= -p CHANGELOG.md)`

# Note that we're setting GOPATH to a temporary location when running goreleaser, because
# we want it to start from a clean state even if we've previously run a build - and also
# because during a release, we may need to run this command under another account and we
# don't want to mess up file permissions in the regular GOPATH.
"$GOBIN"/goreleaser --clean --release-notes <(echo "${RELEASE_NOTES}") "$@"
