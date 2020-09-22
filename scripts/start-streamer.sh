#!/bin/bash

# Starts our minimal simulation of the LD streaming service on the specified port.

set -e

PORT=$1

cd $(dirname $0)/../_testservice
make >/dev/null
./testservice start streamer $PORT
