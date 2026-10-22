package relayenv

// Tests for recovering an environment with a real SDK client, against a fake LaunchDarkly that
// rejects revoked keys. The tests in env_context_recover_test.go use a fake client, which reports
// whatever status a test gives it. These tests check that relay and the SDK agree on what a revoked
// key does to the client's status: the SDK reports Off, and a re-key restarts the same client.

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldservices"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldservicesv2"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLaunchDarkly serves FDv2 streaming data to every key it has not revoked, and 401 to every
// request that carries a revoked key. Other requests, such as polling, get 503, so a client gets its
// data from the stream.
type fakeLaunchDarkly struct {
	mu             sync.Mutex
	revoked        map[string]bool
	rejectedPolls  int
	streamControls []interface{ EndAll() }
	server         *httptest.Server
}

func newFakeLaunchDarkly(t *testing.T, revoked ...string) *fakeLaunchDarkly {
	t.Helper()
	data := ldservicesv2.NewServerSDKData().Flags(ldbuilders.NewFlagBuilder("flag").Version(1).On(true).Build())
	f := &fakeLaunchDarkly{revoked: map[string]bool{}}
	for _, key := range revoked {
		f.revoked[key] = true
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		reject := f.revoked[r.Header.Get("Authorization")]
		if reject && strings.HasPrefix(r.URL.Path, "/sdk/poll") {
			f.rejectedPolls++
		}
		f.mu.Unlock()

		switch {
		case reject:
			w.WriteHeader(http.StatusUnauthorized)
		case strings.HasPrefix(r.URL.Path, "/sdk/stream"):
			// A protocol is emptied when a handler takes it, so every connection gets its own.
			protocol := ldservicesv2.NewStreamingProtocol().
				WithIntent(subsystems.ServerIntent{Payload: subsystems.Payload{
					ID: "payload", Target: 0, Code: subsystems.IntentTransferFull, Reason: "payload-missing",
				}}).
				WithPutObjects(data.ToPutObjects()).
				WithTransferred("state", 1)
			handler, control := ldservices.ServerSideStreamingV2ServiceProtocolHandler(protocol)
			f.mu.Lock()
			f.streamControls = append(f.streamControls, control)
			f.mu.Unlock()
			handler.ServeHTTP(w, r)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// revoke makes every later request with key fail with 401, and ends the open streams so that the
// clients reconnect.
func (f *fakeLaunchDarkly) revoke(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked[key] = true
	for _, control := range f.streamControls {
		control.EndAll()
	}
}

// allow makes LaunchDarkly accept key again.
func (f *fakeLaunchDarkly) allow(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.revoked, key)
}

func (f *fakeLaunchDarkly) rejectedPollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rejectedPolls
}

// newRealClientTestEnv makes an environment with a real SDK client that connects to fake. A
// cleanupInterval of zero gives the default interval, which is longer than any of these tests.
func newRealClientTestEnv(
	t *testing.T,
	fake *fakeLaunchDarkly,
	sdkKey config.SDKKey,
	cleanupInterval time.Duration,
) EnvContext {
	t.Helper()
	uri, err := configtypes.NewOptURLAbsoluteFromString(fake.server.URL)
	require.NoError(t, err)
	allConfig := config.Config{}
	allConfig.Main.BaseURI = uri
	allConfig.Main.StreamURI = uri
	allConfig.Main.InitTimeout = configtypes.NewOptDuration(5 * time.Second)
	allConfig.Events.EventsURI = uri

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:        config.EnvConfig{SDKKey: sdkKey},
		AllConfig:        allConfig,
		ClientFactory:    sdks.DefaultClientFactory(),
		ConnectionMapper: mockConnectionMapper{},
		Logger:           slog.Default(),

		ExpiredCredentialCleanupInterval: cleanupInterval,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Close() })
	return env
}

func dataSourceState(env EnvContext) interfaces.DataSourceState {
	if client := env.GetClient(); client != nil {
		return client.GetDataSourceStatus().State
	}
	return ""
}

func TestARealClientCreatedOnARevokedKeyServesAfterAReanchor(t *testing.T) {
	fake := newFakeLaunchDarkly(t, "sdk-revoked")
	env := newRealClientTestEnv(t, fake, "sdk-revoked", 0)

	// Every synchronizer fails on the revoked key, so the build records ErrInitializationFailed, the
	// SDK reports Off, and the middleware refuses every request for the environment.
	require.Eventually(t, func() bool {
		return errors.Is(env.GetInitError(), ld.ErrInitializationFailed)
	}, 5*time.Second, 10*time.Millisecond, "the revoked key must record an initialization error")
	require.Equal(t, interfaces.DataSourceStateOff, dataSourceState(env))
	client := env.GetClient()

	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, "sdk-valid", "", ""), time.Unix(1000, 0))

	require.Eventually(t, func() bool {
		return dataSourceState(env) == interfaces.DataSourceStateValid
	}, 5*time.Second, 10*time.Millisecond, "the client must receive data on the new key")
	assert.NoError(t, env.GetInitError(), "the environment must serve once its client receives data")
	assert.Equal(t, client, env.GetClient(), "the client must be re-keyed rather than rebuilt")
}

func TestARealClientWhoseKeyIsRevokedRecoversAfterAReanchor(t *testing.T) {
	fake := newFakeLaunchDarkly(t)
	env := newRealClientTestEnv(t, fake, "sdk-old", 0)
	require.Eventually(t, func() bool {
		return dataSourceState(env) == interfaces.DataSourceStateValid
	}, 5*time.Second, 10*time.Millisecond, "the client must receive data on its first key")

	client := env.GetClient()

	// LaunchDarkly revokes the key. The stream reconnects and fails, and then polling fails, so the
	// SDK reports Off.
	fake.revoke("sdk-old")
	require.Eventually(t, func() bool {
		return dataSourceState(env) == interfaces.DataSourceStateOff
	}, 5*time.Second, 10*time.Millisecond, "every synchronizer must fail on the revoked key")
	require.Positive(t, fake.rejectedPollCount(), "the client must fall back to polling on the revoked key")

	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, "sdk-new", "", ""), time.Unix(1000, 0))

	require.Eventually(t, func() bool {
		return dataSourceState(env) == interfaces.DataSourceStateValid
	}, 5*time.Second, 10*time.Millisecond, "the environment must receive data on the new key")
	assert.NoError(t, env.GetInitError())
	assert.Equal(t, client, env.GetClient(), "the client must be re-keyed rather than rebuilt")
}

func TestARealClientRecoversWhenLaunchDarklyAcceptsItsKeyAgain(t *testing.T) {
	// LaunchDarkly rejects the key at first, for example a new key that it does not know yet. The
	// anchor does not change, so only the cleanup ticker's retry can start the client again.
	fake := newFakeLaunchDarkly(t, "sdk-pending")
	env := newRealClientTestEnv(t, fake, "sdk-pending", 100*time.Millisecond)

	require.Eventually(t, func() bool {
		return errors.Is(env.GetInitError(), ld.ErrInitializationFailed)
	}, 5*time.Second, 10*time.Millisecond, "the rejected key must record an initialization error")
	client := env.GetClient()

	// The build polled twice: the initializer and the polling synchronizer. A third rejected poll
	// shows that the retry started the synchronizers again with the rejected key.
	require.Eventually(t, func() bool {
		return fake.rejectedPollCount() >= 3
	}, 5*time.Second, 10*time.Millisecond, "the retry must try the key again while LaunchDarkly rejects it")

	fake.allow("sdk-pending")

	require.Eventually(t, func() bool {
		return dataSourceState(env) == interfaces.DataSourceStateValid
	}, 5*time.Second, 10*time.Millisecond, "the client must receive data once LaunchDarkly accepts the key")
	assert.NoError(t, env.GetInitError(), "the environment must serve once its client receives data")
	assert.Equal(t, client, env.GetClient(), "the retry must re-key the client rather than rebuild it")
}
