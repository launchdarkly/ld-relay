#!/bin/bash

set -e

# Standard build.sh for Go projects

source "$(dirname "$0")/setup.sh"

$GO build ./...
