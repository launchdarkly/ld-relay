#!/bin/bash

# The "products-for-release" makefile target does a goreleaser build but doesn't push to DockerHub
$(dirname $0)/run-publish-target.sh products-for-release

# Copy the Docker image that goreleaser just built into the artifacts - we only do
# this in a dry run, because in a real release the image will be available from
# DockerHub anyway so there's no point in attaching it to the release.
image_archive_name=ld-relay-docker-image.tar.gz
sudo docker save launchdarkly/ld-relay:${LD_RELEASE_VERSION} | gzip >${LD_RELEASE_ARTIFACTS_DIR}/${image_archive_name}
