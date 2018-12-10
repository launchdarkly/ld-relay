#!/bin/sh
CONF_FILE=/ldr/ld-relay.conf

if [ ! -f $CONF_FILE ]; then
# only create file if it doesn't exist already

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
" > $CONF_FILE

if [ "$USE_REDIS" == 1 ] || [ "$USE_REDIS" == "true" ]; then
  if [ -z "${REDIS_HOST}" ] && [ -z "${REDIS_PORT}" ] && [ -z "${REDIS_URL}" ]; then
    echo "Choose REDIS_HOST and REDIS_PORT or REDIS_URL"
    exit 1
  fi

  if [ -n "$CACHE_TTL" ]; then
    REDIS_TTL=$CACHE_TTL
  fi

  echo "
[redis]
localTtl = ${REDIS_TTL:-30000}
" >> $CONF_FILE

  if [ -n "$REDIS_HOST" ] || [ -n "$REDIS_PORT" ]; then

    if echo "$REDIS_PORT" | grep -q 'tcp://'; then
      # REDIS_PORT gets set to tcp://$docker_ip:6379 when linking to a redis container
      # default to using those values if they exist
      REDIS_HOST_PART="${REDIS_PORT%:*}"
      REDIS_HOST="${REDIS_HOST_PART##*/}"
      REDIS_PORT="${REDIS_PORT##*:}"
    fi

    echo "
host = \"${REDIS_HOST:-redis}\"
port = ${REDIS_PORT:-6379}
" >> $CONF_FILE

  elif [ -n "${REDIS_URL+x}" ]; then

    echo "
url = "${REDIS_URL}"
" >> $CONF_FILE

  fi
fi

if [ "$USE_DYNAMODB" == 1 ] || [ "$USE_DYNAMODB" == "true" ]; then
  echo "
[dynamoDB]
enabled = true
tableName = \"${DYNAMODB_TABLE:-}\"
localTtl = ${CACHE_TTL:-30000}
" >> $CONF_FILE
fi

if [ "$USE_CONSUL" == 1 ] || [ "$USE_CONSUL" == "true" ]; then
  echo "
[consul]
host = \"${CONSUL_HOST:-localhost}\"
localTtl = ${CACHE_TTL:-30000}
" >> $CONF_FILE
fi

if [ "$USE_EVENTS" == 1 ] || [ "$USE_EVENTS" == "true" ]; then
  echo "
[events]
eventsUri = \"${EVENTS_HOST:-https://events.launchdarkly.com}\"
sendEvents = true
flushIntervalSecs = ${EVENTS_FLUSH_INTERVAL:-5}
samplingInterval = ${EVENTS_SAMPLING_INTERVAL:-0}
capacity = ${EVENTS_CAPACITY:-10000}
" >> $CONF_FILE
fi

for environment in $(env | grep ^LD_ENV_ ); do
  env_name="$(echo "$environment" | sed 's/^LD_ENV_//' | cut -d'=' -f1)"
  env_key="$(eval echo "\$$(echo "$environment" | cut -d'=' -f1)")"
  env_prefix="$(eval echo "\$LD_PREFIX_${env_name}")"
  env_table="$(eval echo "\$LD_TABLE_NAME_${env_name}")"
  env_mobile_key="$(eval echo "\$LD_MOBILE_KEY_${env_name}")"
  env_id="$(eval echo "\$LD_CLIENT_SIDE_ID_${env_name}")"

  echo "
[environment \"$env_name\"]
sdkKey = \"$env_key\"
mobileKey = \"$env_mobile_key\"
envId = \"$env_id\"" >> $CONF_FILE

  if [ -n "$env_prefix" ]; then
    echo "prefix = \"$env_prefix\"" >> $CONF_FILE
  fi
  if [ -n "$env_table" ]; then
    echo "tableName = \"$env_table\"" >> $CONF_FILE
  fi
done

fi

if [ "$USE_DATADOG" == 1 ] || [ "$USE_DATADOG" == "true" ]; then
echo "
[datadog]
enabled = true
statsAddr = \"${DATADOG_STATS_ADDR}\"
traceAddr = \"${DATADOG_TRACE_ADDR}\"
prefix = \"${DATADOG_PREFIX}\"" >> $CONF_FILE
for tag in $(env | grep ^DATADOG_TAG_ ); do
tag_name="$(echo "$tag" | sed 's/^DATADOG_TAG_//' | cut -d'=' -f1)"
tag_val="$(eval echo "\$$(echo "${tag}" | cut -d'=' -f1)")"
echo "tag = \"$tag_name:$tag_val\"" >> $CONF_FILE
done
echo "
" >> $CONF_FILE
fi

if [ "$USE_STACKDRIVER" == 1 ] || [ "$USE_STACKDRIVER" == "true" ]; then
echo "
[stackdriver]
enabled = true
projectID = \"${STACKDRIVER_PROJECT_ID}\"
prefix = \"${STACKDRIVER_PREFIX}\"
" >> $CONF_FILE
fi

if [ "$USE_PROMETHEUS" == 1 ] || [ "$USE_PROMETHEUS" == "true" ]; then
echo "
[prometheus]
enabled = true
port = ${PROMETHEUS_PORT:-8031}
prefix = \"${PROMETHEUS_PREFIX}\"
" >> $CONF_FILE
fi

exec "$@"
