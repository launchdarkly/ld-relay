#!/bin/bash

VERSION_GO=internal/version/version.go
VERSION_GO_TEMP=${VERSION_GO}.tmp
sed "s/const Version =.*/const Version = \"${LD_RELEASE_VERSION}\"/g" ${VERSION_GO} > ${VERSION_GO_TEMP}
mv ${VERSION_GO_TEMP} ${VERSION_GO}
