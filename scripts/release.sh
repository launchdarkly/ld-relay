#!/usr/bin/env bash
# This script updates the version for the version/version.go file

set -uxe
echo "Starting relay release."

VERSION=$1

#Update version in version/version.go
VERSION_GO_TEMP=./version.go.tmp
sed "s/const Version =.*/const Version = \"${VERSION}\"/g"  version/version.go > ${VERSION_GO_TEMP}
mv ${VERSION_GO_TEMP} version/version.go

echo "Done with relay release"

