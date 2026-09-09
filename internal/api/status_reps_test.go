package api

import (
	"encoding/json"
	"testing"

	"github.com/launchdarkly/go-server-sdk/v7/interfaces"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The documented contract is that statusCode is absent, not zero, for a failure that never had one,
// so a probe can assert on its absence. That depends on the omitempty tag, which no assertion about
// a populated status code would catch.
func TestConnectionErrorRepOmitsAnAbsentStatusCode(t *testing.T) {
	marshal := func(rep ConnectionErrorRep) map[string]interface{} {
		data, err := json.Marshal(rep)
		require.NoError(t, err)
		var out map[string]interface{}
		require.NoError(t, json.Unmarshal(data, &out))
		return out
	}

	t.Run("an HTTP failure carries the status code", func(t *testing.T) {
		out := marshal(ConnectionErrorRep{
			Kind:       interfaces.DataSourceErrorKindErrorResponse,
			StatusCode: 503,
			Time:       1600000000000,
		})
		assert.Equal(t, float64(503), out["statusCode"])
	})

	t.Run("a failure with no status code omits the property", func(t *testing.T) {
		out := marshal(ConnectionErrorRep{
			Kind: interfaces.DataSourceErrorKindNetworkError,
			Time: 1600000000000,
		})
		assert.NotContains(t, out, "statusCode")
		assert.Equal(t, "NETWORK_ERROR", out["kind"])
	})
}

// The auto-config block is a pointer so that it disappears outside automatic configuration mode.
// An absent block is what makes an expect clause on it report an unmet assertion rather than an
// unknown field, so the omitempty tag is part of the endpoint's contract.
func TestStatusRepOmitsAnAbsentAutoConfigStatus(t *testing.T) {
	data, err := json.Marshal(StatusRep{Status: "healthy"})
	require.NoError(t, err)

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &out))
	assert.NotContains(t, out, "autoConfigStatus")

	data, err = json.Marshal(StatusRep{
		Status:           "healthy",
		AutoConfigStatus: &AutoConfigStatusRep{State: interfaces.DataSourceStateValid},
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Contains(t, out, "autoConfigStatus")
}
