package autoconfig

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
)

// slowCache answers GetAll with usable content, but only after a delay.
type slowCache struct {
	delay   time.Duration
	content *PutContent
}

func (c *slowCache) GetAll(ctx context.Context) (*PutContent, error) {
	select {
	case <-time.After(c.delay):
		return c.content, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (c *slowCache) SetAll(context.Context, PutContent) error                     { return nil }
func (c *slowCache) Upsert(context.Context, CacheKind, string, interface{}) error { return nil }
func (c *slowCache) Delete(context.Context, CacheKind, string) error              { return nil }
func (c *slowCache) Close() error                                                 { return nil }

// erroringCache fails the read, the way a store does during a failover, even though the store
// still holds a usable configuration.
type erroringCache struct{}

func (erroringCache) GetAll(context.Context) (*PutContent, error) {
	return nil, errors.New("connection refused: store is failing over")
}
func (erroringCache) SetAll(context.Context, PutContent) error                     { return nil }
func (erroringCache) Upsert(context.Context, CacheKind, string, interface{}) error { return nil }
func (erroringCache) Delete(context.Context, CacheKind, string) error              { return nil }
func (erroringCache) Close() error                                                 { return nil }

func oneEnvironmentCacheContent() *PutContent {
	return &PutContent{
		Environments: map[config.EnvironmentID]envfactory.EnvironmentRep{testEnv1.EnvID: testEnv1},
	}
}

// rejectingStreamTest runs a StreamManager against a stream that rejects every request, with the
// given cache, and returns the readiness channel.
func rejectingStreamTest(
	t *testing.T,
	cache Cache,
	configure func(p streamManagerTestParams),
	action func(p streamManagerTestParams, readyCh <-chan error),
) {
	t.Helper()
	handler := httphelpers.HandlerWithStatus(401)
	_, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()

	streamManagerTestWithStreamHandler(t, handler, stream, cache, func(p streamManagerTestParams) {
		p.streamManager.extendedRetryDelay = time.Millisecond
		if configure != nil {
			configure(p)
		}
		action(p, p.streamManager.Start())
	})
}

// A non-positive initTimeout must not mean "give up at once". Zero means "do not block on init"
// everywhere else in Relay's configuration surface, and applying that reading here discarded a
// cached configuration the store was about to deliver -- the give-up cancelled the in-flight
// read on its way out.
func TestNonPositiveInitTimeoutStillWaitsForTheCacheRead(t *testing.T) {
	for _, initTimeout := range []time.Duration{0, -1 * time.Second} {
		t.Run(initTimeout.String(), func(t *testing.T) {
			cache := &slowCache{delay: 300 * time.Millisecond, content: oneEnvironmentCacheContent()}

			rejectingStreamTest(t, cache,
				func(p streamManagerTestParams) { p.streamManager.initTimeout = initTimeout },
				func(p streamManagerTestParams, readyCh <-chan error) {
					// The cached environment still arrives and Relay keeps serving it.
					p.requireMessage()
					p.requireReceivedAllMessage()
					if !helpers.AssertNoMoreValues(t, readyCh, 300*time.Millisecond,
						"Relay gave up despite a usable cached configuration") {
						t.FailNow()
					}
				})
		})
	}
}

// A cache Relay could not read is not an empty cache. A failover or a DNS blip at startup must
// not make Relay conclude it has nothing to serve, because the store may hold a perfectly good
// configuration and there is no second read.
func TestUnreadableCacheDoesNotCountAsEmpty(t *testing.T) {
	rejectingStreamTest(t, erroringCache{}, nil,
		func(p streamManagerTestParams, readyCh <-chan error) {
			if !helpers.AssertNoMoreValues(t, readyCh, 700*time.Millisecond,
				"Relay gave up on a cache read failure, which is not the same as an empty cache") {
				t.FailNow()
			}
			p.mockLog.AssertMessageMatch(t, true, ldlog.Warn, "cache read failed")
			p.mockLog.AssertMessageMatch(t, false, ldlog.Error, "no cached configuration is available")
		})
}

// A reachable, genuinely empty cache is still grounds for giving up -- the other half of the
// distinction above.
func TestEmptyCacheStillGivesUp(t *testing.T) {
	rejectingStreamTest(t, noopTestCache{}, nil,
		func(p streamManagerTestParams, readyCh <-chan error) {
			err := helpers.RequireValue(t, readyCh, time.Second,
				"expected Relay to give up on a rejected credential with an empty cache")
			require.Error(t, err)
			p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "no cached configuration is available")
		})
}

// An entry holding only filters is nothing Relay can serve: no environments means no credential
// to accept and no flag data to answer with. It must not count as a configuration.
func TestFiltersOnlyCacheIsNotAConfiguration(t *testing.T) {
	cache := &slowCache{content: &PutContent{
		Filters: map[config.FilterID]envfactory.FilterRep{"filter-1": {}},
	}}

	rejectingStreamTest(t, cache, nil,
		func(p streamManagerTestParams, readyCh <-chan error) {
			err := helpers.RequireValue(t, readyCh, time.Second,
				"expected Relay to give up: a filters-only entry leaves it with zero environments")
			require.Error(t, err)
			p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "no cached configuration is available")
		})
}
