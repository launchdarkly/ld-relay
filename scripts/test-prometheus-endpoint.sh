#!/bin/bash

# This script performs a smoke test for Relay data export to Prometheus. It does not run Prometheus,
# but starts Relay with Prometheus export enabled and then verifies that a metric is available from
# Relay's /metrics endpoint.

set -e

FAKE_LD_PORT=8100
RELAY_PORT=8101
RELAY_METRICS_PORT=8102

go build .

TEMP_DIR=$(mktemp -d -t ld-relay-XXXXXXXXX)
trap "rm -rf $TEMP_DIR" EXIT

RELAY_BASE_VARS="\
  PORT=${RELAY_PORT} \
  LD_LOG_LEVEL="debug" \
  LD_ENV_test=fake-sdk-key \
  LD_CLIENT_SIDE_ID_test="fake-env-id" \
  USE_PROMETHEUS=1 \
  PROMETHEUS_PORT=${RELAY_METRICS_PORT} \
  DISABLE_INTERNAL_USAGE_METRICS=1 \
"

echo
echo "starting Relay"
echo

$(dirname $0)/start-streamer.sh ${FAKE_LD_PORT}
RELAY_PID=$($(dirname $0)/start-relay.sh ${FAKE_LD_PORT} ${TEMP_DIR}/relay.out ${RELAY_BASE_VARS} TLS_MIN_VERSION=1.2)
trap "kill ${RELAY_PID} && $(dirname $0)/stop-streamer.sh && rm -rf ${TEMP_DIR}" EXIT

# make an SDK endpoint request, causing request metric to be incremented (we don't care about the output)
echo
echo "querying Relay SDK endpoint to generate a metric"
curl --fail --silent -X REPORT -H 'content-type:application/json' --data '{"key":"test-user"}' \
  http://localhost:${RELAY_PORT}/sdk/evalx/fake-env-id/user >/dev/null

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

echo
echo "pass!"
