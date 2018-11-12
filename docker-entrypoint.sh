#!/bin/sh
if [ ! -f /etc/ld-relay.conf ]; then
# only create /etc/ld-relay.conf if it doesn't exist already

if ! env | grep -q LD_ENV_ ; then
  echo "WARNING: at least one LD_ENV_ should be set" >&2
fi

echo "
[main]
streamUri = \"${STREAM_URI:-https://stream.launchdarkly.com}\"
baseUri = \"${BASE_URI:-https://app.launchdarkly.com}\"
exitOnError = ${EXIT_ON_ERROR:-false}
port = 8030
heartbeatIntervalSecs = ${HEARTBEAT_INTERVAL:-15}
" > /ldr/ld-relay.conf

if [ "$USE_REDIS" = 1 ]; then
if [ -z "${REDIS_HOST}" ] && [ -z "${REDIS_URL}" ]; then echo "Choose REDIS_HOST and REDIS_PORT or REDIS_URL"; exit 1; fi
if echo "$REDIS_PORT" | grep -q 'tcp://'; then
# REDIS_PORT gets set to tcp://$docker_ip:6379 when linking to a redis container
# default to using those values if they exist
REDIS_HOST_PART="${REDIS_PORT%:*}"
REDIS_HOST="${REDIS_HOST_PART##*/}"
REDIS_PORT="${REDIS_PORT##*:}"
echo "
[redis]
host = \"${REDIS_HOST:-redis}\"
port = ${REDIS_PORT:-6379}
" >> /ldr/ld-relay.conf
elif [ -n "${REDIS_URL+x}" ]; then
echo "
[redis]
url = \"${REDIS_URL:-redis://:password@redis:6380/0}\"
" >> /ldr/ld-relay.conf
fi
echo "localTtl = ${REDIS_TTL:-30000}" >> /ldr/ld-relay.conf
fi


if [ "$USE_EVENTS" = 1 ]; then
echo "
[events]
eventsUri = \"${EVENTS_HOST:-https://events.launchdarkly.com}\"
sendEvents = ${EVENTS_SEND:-true}
flushIntervalSecs = ${EVENTS_FLUSH_INTERVAL:-5}
samplingInterval = ${EVENTS_SAMPLING_INTERVAL:-0}
capacity = ${EVENTS_CAPACITY:-10000}
" >> /ldr/ld-relay.conf
fi

for environment in $(env | grep ^LD_ENV_ ); do
env_name="$(echo "$environment" | sed 's/^LD_ENV_//' | cut -d'=' -f1)"
env_key="$(eval echo "\$$(echo "$environment" | cut -d'=' -f1)")"
env_prefix="$(eval echo "\$LD_PREFIX_${env_name}")"
env_mobile_key="$(eval echo "\$LD_MOBILE_KEY_${env_name}")"
env_id="$(eval echo "\$LD_CLIENT_SIDE_ID_${env_name}")"

echo "
[environment \"$env_name\"]
sdkKey = \"$env_key\"
mobileKey = \"$env_mobile_key\"
envId = \"$env_id\"" >> /ldr/ld-relay.conf

if [ -n "$env_prefix" ]; then
echo "prefix = \"$env_prefix\"" >> /ldr/ld-relay.conf
fi
done

fi

if [ "$USE_DATADOG" = 1 ]; then
echo "
[datadog]
enabled = true
statsAddr = \"${DATADOG_STATS_ADDR}\"
traceAddr = \"${DATADOG_TRACE_ADDR}\"
prefix = \"${DATADOG_PREFIX}\"" >> /ldr/ld-relay.conf
for tag in $(env | grep ^DATADOG_TAG_ ); do
tag_name="$(echo "$tag" | sed 's/^DATADOG_TAG_//' | cut -d'=' -f1)"
tag_val="$(eval echo "\$$(echo "${tag}" | cut -d'=' -f1)")"
echo "tag = \"$tag_name:$tag_val\"" >> /ldr/ld-relay.conf
done
echo "
" >> /ldr/ld-relay.conf
fi

if [ "$USE_STACKDRIVER" = 1 ]; then
echo "
[stackdriver]
enabled = true
projectID = \"${STACKDRIVER_PROJECT_ID}\"
prefix = \"${STACKDRIVER_PREFIX}\"
" >> /ldr/ld-relay.conf
fi

if [ "$USE_PROMETHEUS" = 1 ]; then
echo "
[prometheus]
enabled = true
port = ${PROMETHEUS_PORT:-8031}
prefix = \"${PROMETHEUS_PREFIX}\"
" >> /ldr/ld-relay.conf
fi

exec "$@"
