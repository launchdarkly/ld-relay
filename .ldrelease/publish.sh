#!/bin/bash

# See comments in .ldrelease/config.yml for an explanation of the build/release process.

# the "publish" makefile target pushes the image to Docker
$(dirname $0)/run-publish-target.sh publish
