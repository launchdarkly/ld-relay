#!/bin/bash

# See comments in .ldrelease/config.yml for an explanation of the build/release process.

VERSION_GO=relay/version/version.go
VERSION_GO_TEMP=${VERSION_GO}.tmp
sed "s/const Version =.*/const Version = \"${LD_RELEASE_VERSION}\"/g" ${VERSION_GO} > ${VERSION_GO_TEMP}
mv ${VERSION_GO_TEMP} ${VERSION_GO}
