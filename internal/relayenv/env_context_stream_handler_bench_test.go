package relayenv

// Benchmarks the stream-handler lookup, which moved from a prebuilt cache to a per-request build so
// that the accepted set can be re-checked at request time (refer to acceptsForStream).
//
// BenchmarkStreamHandlerCachedLookup reproduces the path that was replaced: one read-locked lookup in
// a map keyed by credential. BenchmarkGetStreamHandlerV1 is what replaced it.

import (
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/credential"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v9/internal/streams"

	"github.com/stretchr/testify/require"
)

// sinkHandler keeps the benchmarked handler alive past the loop. Without it escape analysis can
// stack-allocate the closure a per-request build returns, which is not what happens in production,
// where the handler is returned across an interface boundary and then served.
var sinkHandler http.Handler

func benchStreamHandlerEnv(b *testing.B, sp streams.StreamProvider) EnvContext {
	b.Helper()
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:        st.EnvMain.Config,
		AllConfig:        config.Config{},
		ClientFactory:    testclient.FakeLDClientFactory(true),
		StreamProviders:  []streams.StreamProvider{sp},
		ConnectionMapper: mockConnectionMapper{},
		Logger:           slog.Default(),
	}, nil)
	require.NoError(b, err)
	return env
}

func BenchmarkGetStreamHandlerV1(b *testing.B) {
	sp := streams.NewStreamProvider(basictypes.JSClientPingStream, time.Hour, 0)
	env := benchStreamHandlerEnv(b, sp)
	defer env.Close()

	envID := st.EnvMain.Config.EnvID
	env.ReconcileCredentials(mustAcceptedSetForBench(b, st.EnvMain.Config.SDKKey, envID))

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sinkHandler = env.GetStreamHandlerV1(sp, envID)
	}
	if sinkHandler == nil {
		b.Fatal("expected a handler")
	}
}

func BenchmarkStreamHandlerCachedLookup(b *testing.B) {
	sp := streams.NewStreamProvider(basictypes.JSClientPingStream, time.Hour, 0)
	envID := st.EnvMain.Config.EnvID

	// The replaced shape: one map per provider, keyed by credential, guarded by the environment's lock.
	var mu sync.RWMutex
	cache := map[streams.StreamProvider]map[credential.SDKCredential]http.Handler{
		sp: {envID: sp.HandlerV1(envID)},
	}
	lookup := func(p streams.StreamProvider, cred credential.SDKCredential) http.Handler {
		mu.RLock()
		defer mu.RUnlock()
		return cache[p][cred]
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sinkHandler = lookup(sp, envID)
	}
	if sinkHandler == nil {
		b.Fatal("expected a handler")
	}
}

func mustAcceptedSetForBench(b *testing.B, anchor config.SDKKey, envID config.EnvironmentID) credential.AcceptedSet {
	b.Helper()
	set, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: anchor}).
		WithEnvironmentID(envID).
		Build()
	require.NoError(b, err)
	return set
}

// BenchmarkGetStreamHandlerV2ServerSide uses the server-side provider, which wraps its handler in an
// init-deadline closure and, on the V2 path, a basis-header closure. It is the heaviest build of the
// providers relay registers.
func BenchmarkGetStreamHandlerV2ServerSide(b *testing.B) {
	sp := streams.NewStreamProvider(basictypes.ServerSideStream, time.Hour, 0)
	env := benchStreamHandlerEnv(b, sp)
	defer env.Close()

	sdkKey := st.EnvMain.Config.SDKKey
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sinkHandler = env.GetStreamHandlerV2(sp, sdkKey)
	}
	if sinkHandler == nil {
		b.Fatal("expected a handler")
	}
}
