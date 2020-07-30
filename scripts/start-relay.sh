#!/bin/bash

# start-relay.sh <output file path> <var=value...>
#
# Starts a Relay process in the background, directing its output to a temporary file and waiting until the
# output in the file indicates that it has started. Then writes the PID to standard output.

OUT_FILE="$1"
shift

# Start a fake LaunchDarkly service endpoint that simply returns a single empty stream put event -
# that's enough to ensure that the SDK client can initialize. The nc command will close the socket
# after responding, so the SDK will go into "reconnecting" mode, but that shouldn't affect our test.
FAKE_LD_PORT=8100
TEMP_FILE_STREAM_DATA=$(mktemp)
cat >${TEMP_FILE_STREAM_DATA} <<EOF
HTTP/1.1 200 OK
Content-Type: text/event-stream

event: put
data: {"data":{}}


EOF

if [ "$OSTYPE" == "linux-gnu" ]; then
  nc -l -p ${FAKE_LD_PORT} <"${TEMP_FILE_STREAM_DATA}" >/dev/null &
else
  nc -l ${FAKE_LD_PORT} <"${TEMP_FILE_STREAM_DATA}" >/dev/null &
fi


export $@
export STREAM_URI=http://localhost:${FAKE_LD_PORT}
touch ${OUT_FILE}
./ld-relay --from-env >>${OUT_FILE} 2>&1 &
PID=$!

( tail -f -n +1 $OUT_FILE & ) | sed '/Successfully initialized/ q' 1>&2

echo $PID
