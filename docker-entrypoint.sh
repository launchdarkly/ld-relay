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
" > /etc/ld-relay.conf

if [ "$USE_REDIS" = 1 ]; then
if echo "$REDIS_PORT" | grep -q 'tcp://'; then
  # REDIS_PORT gets set to tcp://$docker_ip:6379 when linking to a redis container
  # default to using those values if they exist
  REDIS_HOST_PART="${REDIS_PORT%:*}"
  REDIS_HOST="${REDIS_HOST_PART##*/}"
  REDIS_PORT="${REDIS_PORT##*:}"
fi

echo "
[redis]
host = \"${REDIS_HOST:-redis}\"
port = ${REDIS_PORT:-6379}
localTtl = ${REDIS_TTL:-30000}
" >> /etc/ld-relay.conf
fi

if [ "$USE_EVENTS" = 1 ]; then
echo "
[events]
eventsUri = \"${EVENTS_HOST:-https://events.launchdarkly.com}\"
sendEvents = ${EVENTS_SEND:-true}
flushIntervalSecs = ${EVENTS_FLUSH_INTERVAL:-5}
samplingInterval = ${EVENTS_SAMPLING_INTERVAL:-0}
capacity = ${EVENTS_CAPACITY:-10000}
" >> /etc/ld-relay.conf
fi

for environment in $(env | grep LD_ENV_ ); do
env_name="$(echo "$environment" | sed 's/^LD_ENV_//' | cut -d'=' -f1)"
env_key="$(eval echo "\$$(echo "$environment" | cut -d'=' -f1)")"
env_prefix="$(eval echo "\$LD_PREFIX_${env_name}")"
env_mobile_key="$(eval echo "\$LD_MOBILE_KEY_${env_name}")"
env_id="$(eval echo "\$LD_CLIENT_SIDE_ID_${env_name}")"

echo "
[environment \"$env_name\"]
apiKey = \"$env_key\"
mobileKey = \"$env_mobile_key\"
envId = \"$env_id\"" >> /etc/ld-relay.conf

if [ -n "$env_prefix" ]; then
echo "prefix = \"$env_prefix\"" >> /etc/ld-relay.conf
fi
done

fi

exec "$@"
