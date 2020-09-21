#!/bin/bash

set -e

# start-relay.sh <LD port> <output file path> <var=value...>
#
# Starts a Relay process in the background, directing its output to a temporary file and waiting until the
# output in the file indicates that it has started. Then writes the PID to standard output.

FAKE_LD_PORT="$1"
OUT_FILE="$2"
shift
shift

export $@
export STREAM_URI=http://localhost:${FAKE_LD_PORT}
touch ${OUT_FILE}
./ld-relay --from-env >>${OUT_FILE} 2>&1 &
PID=$!

( tail -f -n +1 $OUT_FILE & ) | sed '/Successfully initialized/ q' 1>&2

echo $PID
