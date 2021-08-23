#!/bin/bash

# Called from publish.sh or publish-dry-run.sh - parameter is the Makefile target to run
TARGET=$1

# Note that Docker commands in this script are being sudo'd. That's because we are
# already running inside a container, and rather than trying to run a whole nested
# Docker daemon inside that container, we are sharing the host's Docker daemon. But
# the mechanism for doing so involves sharing a socket path (docker.sock) that is
# only accessible by root.

# Doing a Docker login is only relevant if we're doing a real release rather than a
# dry run, but we'll still do it even if it's a dry run, because that proves that
# our credentials are right so a release would theoretically succeed.
docker_username="$(cat "${LD_RELEASE_SECRETS_DIR}/docker_username")"
cat "${LD_RELEASE_SECRETS_DIR}/docker_token" | sudo docker login --username "${docker_username}" --password-stdin

sudo PATH=$PATH make $TARGET

# Goreleaser puts all the artifacts in ./dist - copying them to $LD_RELEASE_ARTIFACTS_DIR
# causes Releaser to pick them up and attach them to the release in GitHub. However, don't
# copy every file from ./dist because Goreleaser also puts temporary build products there
# that we don't want.
mkdir -p ./artifacts
cp ./dist/*.deb ./dist/*.rpm ./dist/*.tar.gz ./dist/*.txt ${LD_RELEASE_ARTIFACTS_DIR}
