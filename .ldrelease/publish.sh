#!/bin/bash

# the "publish" makefile target pushes the image to Docker
$(dirname $0)/run-publish-target.sh publish
