#!/bin/bash

# This script will be run in a CircleCI Linux job triggered by Releaser, using the Docker image
# that is specified in our .ldrelease/config.yml file.

sudo apt-get -q update
sudo apt-get -y install rpm

# "make release" builds all the artifacts but doesn't push anything to Docker
make release

# "make publish" pushes the image to Docker
# DOCKER_USERNAME and DOCKER_PASSWORD environment variables come from CircleCI project settings
docker login -u="$DOCKER_USERNAME" -p="$DOCKER_PASSWORD"
make publish

# Goreleaser puts all the artifacts in ./dist - copying them to ./artifacts causes Releaser to
# pick them up and attach them to the release in GitHub. However, don't copy every file from
# ./dist because Goreleaser also puts temporary build products there that we don't want.
mkdir -p ./artifacts
cp ./dist/*.deb ./dist/*.rpm ./dist/*.tar.gz ./dist/*.txt ./artifacts
