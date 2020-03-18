#!/bin/bash

# This script performs a smoke test for Relay data export to Prometheus. It does not run Prometheus,
# but starts Relay with Prometheus export enabled and then verifies that a metric is available from
# Relay's /metrics endpoint.

set -e

FAKE_LD_PORT=8100
RELAY_PORT=8101
RELAY_METRICS_PORT=8102

# Start a fake LaunchDarkly service endpoint that simply returns a single empty stream put event -
# that's enough to ensure that the SDK client can initialize. The nc command will close the socket
# after responding, so the SDK will go into "reconnecting" mode, but that shouldn't affect our test.
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

go build ./cmd/ld-relay

# start Relay as a background process
echo "starting Relay"
PORT=${RELAY_PORT} \
  STREAM_URI=http://localhost:${FAKE_LD_PORT} \
  LD_LOG_LEVEL="debug" \
  LD_LOG_LEVEL_test="none" \
  LD_ENV_test="fake-sdk-key" \
  LD_CLIENT_SIDE_ID_test="fake-env-id" \
  USE_PROMETHEUS=1 \
  PROMETHEUS_PORT=${RELAY_METRICS_PORT} \
  ./ld-relay --from-env &
RELAY_PID=$!
trap "kill ${RELAY_PID}" exit

# wait for relay to start
echo
echo "waiting for Relay /status endpoint"
fail_count=0
max_attempts=10
while true; do
  curl --silent --show-error --fail http://localhost:${RELAY_PORT}/status >/dev/null && break
  let "fail_count += 1"
  if [[ ${fail_count} -gt ${max_attempts} ]]; then
    echo "Relay did not start after ${max_attempts} seconds; failing"
    exit 1
  fi
  echo "...Relay not ready, retrying"
  sleep 1
done
echo "Relay started"

# make an SDK endpoint request, causing request metric to be incremented (we don't care about the output)
echo
echo "querying Relay SDK endpoint to generate a metric"
fail_count=0
max_attempts=10
while true; do
  curl --fail --silent -X REPORT -H 'content-type:application/json' --data '{"key":"test-user"}' \
    http://localhost:${RELAY_PORT}/sdk/evalx/fake-env-id/user >/dev/null && break
  let "fail_count += 1"
  if [[ ${fail_count} -gt ${max_attempts} ]]; then
    echo "SDK client did not start after ${max_attempts} seconds; failing"
    exit 1
  fi
  echo "...client not ready, retrying"
  sleep 1
done
echo "OK"

# hit the Prometheus exporter endpoint - allow a couple of retries since there can be a lag for the data
echo
echo "querying Relay /metrics endpoint"
TEMP_FILE_METRICS=$(mktemp)
fail_count=0
max_attempts=10
while true; do
  curl --fail --silent http://localhost:${RELAY_METRICS_PORT}/metrics >${TEMP_FILE_METRICS} # endpoint should return 200 even if there's no data
  grep 'env="test",method="REPORT",platformCategory="browser",route="_sdk_evalx_{envId}_user"' <${TEMP_FILE_METRICS} && break
  let "fail_count += 1"
  if [[ $fail_count -gt ${max_attempts} ]]; then
    echo "metrics did not show up after ${max_attempts} seconds; failing"
    echo
    echo "last output from metrics endpoint was:"
    cat "${TEMP_FILE_METRICS}"
    exit 1
  fi
  echo "...got no data, retrying"
  sleep 1
done
echo "got metrics data from endpoint:"
cat "${TEMP_FILE_METRICS}"
