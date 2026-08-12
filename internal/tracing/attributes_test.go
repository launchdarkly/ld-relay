package tracing

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/stretchr/testify/assert"
)

// Span attribute keys are what operators write TraceQL against, so the wire names are a contract.
// Asserting the literal strings here means a rename cannot happen as a side effect of renaming the Go
// identifier, and it keeps the whole naming scheme visible in one place.
//
// The scheme: keys that describe the caller are launchdarkly.<thing>, keys specific to Relay's own
// work are launchdarkly.relay.<thing>, counts end in .count and byte sizes end in .size.
func TestSpanAttributeKeyNames(t *testing.T) {
	expected := map[attribute.Key]string{
		SDKKindKey:            "launchdarkly.sdk.kind",
		AuthResultKey:         "launchdarkly.relay.auth.result",
		FlagCountKey:          "relay.flags.count",
		EventsKindKey:         "launchdarkly.relay.events.kind",
		StoreKeyKey:           "launchdarkly.relay.store.key",
		PayloadEventsKey:      "relay.payload.events",
		PayloadBytesKey:       "relay.payload.bytes",
		SingleflightSharedKey: "launchdarkly.relay.singleflight.shared",
		SingleflightWaitMSKey: "relay.singleflight.wait_ms",
	}

	for key, want := range expected {
		assert.Equal(t, want, string(key))
	}
}

// Span names stay short and unprefixed: they are display strings in a trace waterfall rather than
// queryable dimensions, and the semantic conventions do not namespace them either. They read
// object-then-verb so that related spans sort together.
func TestSpanNames(t *testing.T) {
	assert.Equal(t, "relay.auth", SpanAuth)
	assert.Equal(t, "relay.store.snapshot", SpanStoreSnapshot)
	assert.Equal(t, "relay.store.get_all", SpanStoreGetAll)
	assert.Equal(t, "relay.store.get", SpanStoreGet)
	assert.Equal(t, "relay.evaluate_flags", SpanEvaluateFlags)
	assert.Equal(t, "relay.events.dispatch", SpanEventsDispatch)
	assert.Equal(t, "relay.payload.serialize", SpanSerializePayload)
	assert.Equal(t, "relay.response.write", SpanWriteResponse)
	assert.Equal(t, "relay.singleflight.wait", SpanSingleflightWait)
}
