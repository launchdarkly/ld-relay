#!/bin/bash

set -eu

# Verifies that Relay can enforce a minimum TLS version in secure mode.

TEMP_DIR=$(mktemp -d -t ld-relay-XXXXXXXXX)
trap "rm -rf $TEMP_DIR" EXIT

CA_KEY_FILE=${TEMP_DIR}/ca.key
CA_CERT_FILE=${TEMP_DIR}/ca.crt
KEY_FILE=${TEMP_DIR}/key.key
CSR_FILE=${TEMP_DIR}/cert.csr
CERT_FILE=${TEMP_DIR}/cert.crt
CERT_PROPS="/C=US/ST=CA/L=CA/O=LaunchDarkly/OU=none/CN=ld-relay-test/emailAddress=team@launchdarkly.com"

openssl req -nodes -x509 -newkey rsa:2048 -keyout ${CA_KEY_FILE} -out ${CA_CERT_FILE} -subj "${CERT_PROPS}" 2>/dev/null
openssl req -nodes -newkey rsa:2048 -keyout ${KEY_FILE} -out ${CSR_FILE} -subj "${CERT_PROPS}" 2>/dev/null
openssl x509 -req -in ${CSR_FILE} -CA ${CA_CERT_FILE} -CAkey ${CA_KEY_FILE} -CAcreateserial -out ${CERT_FILE} 2>/dev/null

RELAY_PORT=8103
RELAY_BASE_VARS="\
  PORT=${RELAY_PORT} \
  TLS_ENABLED=1 \
  TLS_CERT=${CERT_FILE} \
  TLS_KEY=${KEY_FILE} \
  LD_ENV_test=fake-sdk-key \
"
STATUS_ENDPOINT=https://localhost:${RELAY_PORT}/status

echo
echo "starting Relay with TLS_MIN_VERSION=1.2"
echo

RELAY_PID=$($(dirname $0)/start-relay.sh ${TEMP_DIR}/relay1.out ${RELAY_BASE_VARS} TLS_MIN_VERSION=1.2)
trap "kill ${RELAY_PID}" EXIT

# Note, for unknown reasons these curl tests do not work reliably with HTTP2, hence --http1.1

echo
echo "verifying that a TLS 1.2 request succeeds"
curl -s --insecure --http1.1 ${STATUS_ENDPOINT} >/dev/null || (echo "TLS 1.2 request failed, should have succeeded"; exit 1)
echo "...correct"

echo "verifying that a TLS 1.1 request does not succeed"
curl -s --insecure --http1.1 --tls-max 1.1 ${STATUS_ENDPOINT} && (echo "TLS 1.1 request succeeded but should have failed"; exit 1)
echo "...correct"

kill ${RELAY_PID}

echo
echo "starting Relay with TLS_MIN_VERSION not set"
echo
RELAY_PID=$($(dirname $0)/start-relay.sh ${TEMP_DIR}/relay2.out ${RELAY_BASE_VARS})
trap "kill ${RELAY_PID}" EXIT

echo
echo "verifying that a TLS 1.2 request succeeds"
curl -s --insecure --http1.1 ${STATUS_ENDPOINT} >/dev/null || (echo "TLS 1.2 request failed, should have succeeded"; exit 1)
echo "...correct"

echo "verifying that a TLS 1.1 request succeeds"
curl -s --insecure --tls-max 1.1 --tlsv1.1 --http1.1 ${STATUS_ENDPOINT} >/dev/null || (echo "TLS 1.1 request failed, should have succeeded"; exit 1)
echo "...correct"

echo
echo "pass!"
