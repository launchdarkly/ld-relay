//go:build redis_unit_tests
// +build redis_unit_tests

package autoconfigcache

// Verifies that a multi-key environment written to the real Redis-backed AutoConfig cache survives a
// process restart: a fresh StreamManager, given the same Redis cache but a config stream that delivers
// nothing, reloads the environment from the cache with its sdkKeys[]/mobileKeys[] arrays intact.
//
// This is the restart-survival half of the "cache integrity across restart" scenario (SDK-2609 #10);
// the malformed-put-preserves-cache half is covered by the StreamManager unit test
// TestMalformedCredentialPayloadPreservesEnvironmentCache. It exercises the production classes
// (StreamManager + redisStore) against an actual Redis, with no test doubles for the cache itself.
//
// Requires a Redis server on localhost (the redis_unit_tests build tag; CI provides one).

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/configsource"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	restartRedisURL   = "redis://localhost:6379"
	restartCacheKey   = "sdk2609-restart-reload-test"
	restartProtocolV2 = 2
)

// capturingHandler is a minimal autoconfig.MessageHandler that records the environments it is told to
// add, so the test can inspect what a StreamManager reloaded from the cache.
type capturingHandler struct {
	added chan envfactory.EnvironmentParams
}

func newCapturingHandler() *capturingHandler {
	return &capturingHandler{added: make(chan envfactory.EnvironmentParams, 10)}
}

func (h *capturingHandler) AddEnvironment(params envfactory.EnvironmentParams)    { h.added <- params }
func (h *capturingHandler) UpdateEnvironment(params envfactory.EnvironmentParams) { h.added <- params }
func (h *capturingHandler) DeleteEnvironment(config.EnvironmentID)                {}
func (h *capturingHandler) ReceivedAllEnvironments()                              {}
func (h *capturingHandler) AddFilter(envfactory.FilterParams)                     {}
func (h *capturingHandler) DeleteFilter(config.FilterID)                          {}

func TestConcurrentKeysCacheReloadSurvivesRestart(t *testing.T) {
	const (
		envID     = config.EnvironmentID("multikey-env")
		anchorSDK = config.SDKKey("sdk-anchor")
		extraSDK  = config.SDKKey("sdk-extra")
		anchorMob = config.MobileKey("mob-anchor")
		extraMob  = config.MobileKey("mob-extra")
	)

	cacheConfig := func() config.Config {
		cfg := config.Config{}
		cfg.AutoConfig.Key = config.AutoConfigKey("test-key")
		cfg.AutoConfig.CacheKey = restartCacheKey
		cfg.Redis.URL, _ = configtypes.NewOptURLAbsoluteFromString(restartRedisURL)
		return cfg
	}

	loggers := ldlog.NewDisabledLoggers()
	httpConfig, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, config.HTTPConfig{}, nil, "", loggers)
	require.NoError(t, err)

	newStreamManager := func(streamURL string, store Store, handler autoconfig.MessageHandler) *autoconfig.StreamManager {
		u, parseErr := url.Parse(streamURL)
		require.NoError(t, parseErr)
		return autoconfig.NewStreamManager(cacheConfig().AutoConfig.Key, u, handler, httpConfig,
			time.Millisecond, restartProtocolV2, loggers, store)
	}

	// A multi-key environment (anchor + one extra SDK key, anchor + one extra mobile key) via the array
	// wire format.
	rep := envfactory.EnvironmentRep{
		EnvID:    envID,
		EnvKey:   "multikey",
		EnvName:  "Multi-Key Env",
		ProjKey:  "multikey-proj",
		ProjName: "Multi-Key Project",
		SDKKey:   envfactory.SDKKeyRep{Value: anchorSDK},
		MobKey:   anchorMob,
		SDKKeys: []envfactory.ConcurrentKeyRep{
			{Key: "anchor-sdk", Value: string(anchorSDK)},
			{Key: "extra-sdk", Value: string(extraSDK)},
		},
		MobileKeys: []envfactory.ConcurrentKeyRep{
			{Key: "anchor-mob", Value: string(anchorMob)},
			{Key: "extra-mob", Value: string(extraMob)},
		},
		Version: 1,
	}

	// Start from a clean cache so a previous run's entry can't stand in for this run's write.
	seed, err := NewStore(cacheConfig(), loggers)
	require.NoError(t, err)
	require.NoError(t, seed.SetAll(context.Background(), autoconfig.PutContent{}))
	require.NoError(t, seed.Close())

	// First run: a StreamManager receives the multi-key put over its stream and persists it to Redis.
	store1, err := NewStore(cacheConfig(), loggers)
	require.NoError(t, err)
	putEvent := configsource.MakeAutoConfigPutEvent(rep)
	liveStream := configsource.NewRACMock(t, &putEvent)
	sm1 := newStreamManager(liveStream.URL, store1, newCapturingHandler())
	helpers.RequireValue(t, sm1.Start(), 5*time.Second, "timed out waiting for the first stream to be ready")

	require.Eventually(t, func() bool {
		content, _ := store1.GetAll(context.Background())
		if content == nil {
			return false
		}
		_, ok := content.Environments[envID]
		return ok
	}, 5*time.Second, 10*time.Millisecond, "the multi-key env was not persisted to the Redis cache")

	sm1.Close() // simulate process shutdown (also closes store1's Redis client)

	// Second run ("after the restart"): a fresh StreamManager with the same Redis cache but a config
	// stream that connects and delivers nothing, so the cache is the only possible source.
	store2, err := NewStore(cacheConfig(), loggers)
	require.NoError(t, err)
	silentStream := configsource.NewRACMock(t, nil)
	handler2 := newCapturingHandler()
	sm2 := newStreamManager(silentStream.URL, store2, handler2)
	defer sm2.Close()
	helpers.RequireValue(t, sm2.Start(), 5*time.Second, "timed out waiting for the second stream to be ready")

	reloaded := helpers.RequireValue(t, handler2.added, 5*time.Second,
		"the environment was not reloaded from the Redis cache after restart")
	assert.Equal(t, envID, reloaded.EnvID)

	// The multi-key arrays survived the cache round-trip: both the anchor and the non-anchor key are
	// present in the reloaded accepted set, for SDK and mobile keys alike.
	var sdkValues []config.SDKKey
	for _, k := range reloaded.AcceptedSDKKeys {
		sdkValues = append(sdkValues, k.Value)
	}
	assert.ElementsMatch(t, []config.SDKKey{anchorSDK, extraSDK}, sdkValues)

	var mobValues []config.MobileKey
	for _, k := range reloaded.AcceptedMobileKeys {
		mobValues = append(mobValues, k.Value)
	}
	assert.ElementsMatch(t, []config.MobileKey{anchorMob, extraMob}, mobValues)
}
