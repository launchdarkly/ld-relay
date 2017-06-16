#!/bin/sh

set -e

if [ -n "$CIRCLE_TAG" ]; then
  TAG="$CIRCLE_TAG"
elif [ -n "$CIRCLE_BRANCH" ]; then
  TAG="$CIRCLE_BRANCH"
fi

if [ -z "$TAG" ]; then
  echo "Skipping: unable to determine branch or tag"
  exit
fi

if [ -z "$DOCKER_USERNAME" ] || [ -z "$DOCKER_PASSWORD" ] || [ -z "$DOCKER_REPO" ]; then
  echo "Skipping: DOCKER_USERNAME, DOCKER_PASSWORD, DOCKER_REPO environment variables not set"
  exit
fi

# dockerhub requires an email address, but doesn't use it for anything
echo "build@example.com" | docker login -u="$DOCKER_USERNAME" -p="$DOCKER_PASSWORD"
docker tag "ld-relay:$CIRCLE_BUILD_NUM" "$DOCKER_REPO:$TAG"
docker push "$DOCKER_REPO:$TAG"

if [ "$TAG" = "master" ]; then
  # tag the master branch as latest
  docker tag "$DOCKER_REPO:$TAG" "$DOCKER_REPO:latest" 
  docker push "$DOCKER_REPO:latest"
fi
