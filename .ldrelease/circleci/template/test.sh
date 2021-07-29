#!/bin/bash

set -e

# Standard test.sh for Go projects

source "$(dirname "$0")/setup.sh"

$GO test -v -race ./...
