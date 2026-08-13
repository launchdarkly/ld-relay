package relay

import (
	"net/http"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandlerTracerFollowsTheCurrentProvider pins down that the handlers resolve their tracer
// per request rather than once per process. Each handler hoists tracing.Tracer() into a local
// so it pays for one lookup instead of three or four, and the tempting next step -- memoizing
// that lookup in a package-level variable -- would latch the first provider it saw and silently
// stop recording when the provider changes. This test fails if that happens.
func TestHandlerTracerFollowsTheCurrentProvider(t *testing.T) {
	first := installSpanRecorder(t)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		request := func() {
			result, _ := st.DoRequest(st.BuildRequestWithAuth("GET", "/sdk/flags", st.EnvMain.Config.SDKKey, nil), p.relay)
			require.Equal(t, http.StatusOK, result.StatusCode)
		}

		request()
		require.Len(t, spansNamed(first.Ended(), tracing.SpanSerializePayload), 1,
			"the recorder installed before the relay started should see the first request's spans")

		// Swap in a second provider after the relay is already running and serving.
		second := installSpanRecorder(t)
		first.Reset()

		request()

		assert.Len(t, spansNamed(second.Ended(), tracing.SpanSerializePayload), 1,
			"the handler should have resolved its tracer from the provider current at request time")
		assert.Len(t, spansNamed(second.Ended(), tracing.SpanWriteResponse), 1,
			"the response-write span should follow the same tracer as the rest of the handler")
		assert.Empty(t, spansNamed(first.Ended(), tracing.SpanSerializePayload),
			"the replaced provider should no longer receive handler spans")
	})
}
