#!/bin/bash

# run-goreleaser.sh <goreleaser version> <any other goreleaser options>...
#
# Builds the Docker image and all other executables that we intend to publish.
# This also pushes the image to DockerHub unless we have specifically told it not to with
# the --skip-publish option.

GORELEASER_VERSION=$1
if [[ -z "${GORELEASER_VERSION}" ]]; then
  echo "Must set GORELEASER_VERSION before calling this script"
  exit 1
fi
shift

# Get the lines added to the most recent changelog update (minus the first 2 lines)
RELEASE_NOTES=`(GIT_EXTERNAL_DIFF='bash -c "diff --unchanged-line-format=\"\" $2 $5" || true' git log --ext-diff -1 --pretty= -p CHANGELOG.md)`

# Temporarily add a package override to go.mod to fix CVE-2022-41717. In our 6.x releases, we can't just
# have this override in go.mod all the time because it isn't compatible with Go 1.16. But we never use
# Go 1.16 to build our published executables and we do want the fix in those.
cp go.mod go.mod.bak
cp go.sum go.sum.bak
trap "mv go.mod.bak go.mod; mv go.sum.bak go.sum" EXIT
go get golang.org/x/net@v0.4.0
go mod tidy

curl -sL https://git.io/goreleaser | GOPATH=`mktemp -d` VERSION=${GORELEASER_VERSION} bash -s -- \
  --rm-dist --release-notes <(echo "${RELEASE_NOTES}") $@
