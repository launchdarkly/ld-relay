package relay

import (
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"go.opentelemetry.io/otel/attribute"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The semantic-convention attributes the HTTP tracing middleware derives from the request host.
// These are asserted by their wire names, because that is what a backend queries on.
const (
	serverAddressKey = attribute.Key("server.address")
	serverPortKey    = attribute.Key("server.port")
)

// TestRequestSpanReportsRequestHostAsServerAddress guards the argument passed to
// otelmux.Middleware. That argument is the "primary server name", and a non-empty value
// replaces the request host in server.address on every span, so passing a service name there
// makes every trace claim the client connected to that name. Relay passes an empty string so
// that both server.address and server.port come from the same request.
func TestRequestSpanReportsRequestHostAsServerAddress(t *testing.T) {
	recorder := installSpanRecorder(t)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		headers := make(http.Header)
		headers.Set("Authorization", string(st.EnvMain.Config.SDKKey))
		req := st.BuildRequest("GET", "http://relay.example.com:8031/sdk/flags", nil, headers)

		result, _ := st.DoRequest(req, p.relay)
		require.Equal(t, http.StatusOK, result.StatusCode)

		attrs := spanAttrs(rootSpan(t, recorder.Ended()))

		address, ok := attrs[serverAddressKey]
		require.True(t, ok, "request span is missing server.address")
		assert.Equal(t, "relay.example.com", address.AsString())

		port, ok := attrs[serverPortKey]
		require.True(t, ok, "request span is missing server.port")
		assert.Equal(t, int64(8031), port.AsInt64())
	})
}
