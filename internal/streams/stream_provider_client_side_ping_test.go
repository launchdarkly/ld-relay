package streams

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The only difference between the mobile ping stream and the JS client ping stream is which kind of
// authorization credential they support.

func TestStreamProviderMobilePing(t *testing.T) {
	validCredential := sdkauth.New(testMobileKey)
	invalidCredential1 := sdkauth.New(testSDKKey)
	invalidCredential2 := sdkauth.New(testEnvID)

	withStreamProvider := func(t *testing.T, maxConnTime time.Duration, action func(StreamProvider)) {
		sp := NewStreamProvider(basictypes.MobilePingStream, maxConnTime)
		require.NotNil(t, sp)
		defer sp.Close()
		action(sp)
	}

	t.Run("constructor", func(t *testing.T) {
		maxConnTime := time.Hour
		withStreamProvider(t, maxConnTime, func(sp StreamProvider) {
			require.IsType(t, &clientSidePingStreamProvider{}, sp)
			assert.False(t, sp.(*clientSidePingStreamProvider).isJSClient)
			verifyServerProperties(t, sp.(*clientSidePingStreamProvider).server, maxConnTime)
		})
	})

	t.Run("Handler", func(t *testing.T) {
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.NotNil(t, sp.Handler(validCredential))
			assert.Nil(t, sp.Handler(invalidCredential1))
			assert.Nil(t, sp.Handler(invalidCredential2))
		})
	})

	t.Run("Register", func(t *testing.T) {
		store := makeMockStore(nil, nil, nil, nil)
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.Nil(t, sp.Register(invalidCredential1, store, ldlog.NewDisabledLoggers()))
			assert.Nil(t, sp.Register(invalidCredential2, store, ldlog.NewDisabledLoggers()))

			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()
			require.IsType(t, &clientSidePingEnvStreamProvider{}, esp)
		})
	})
}

func TestStreamProviderJSClientPing(t *testing.T) {
	validCredential := sdkauth.New(testEnvID)
	invalidCredential1 := sdkauth.New(testSDKKey)
	invalidCredential2 := sdkauth.New(testMobileKey)

	withStreamProvider := func(t *testing.T, maxConnTime time.Duration, action func(StreamProvider)) {
		sp := NewStreamProvider(basictypes.JSClientPingStream, maxConnTime)
		require.NotNil(t, sp)
		defer sp.Close()
		action(sp)
	}

	t.Run("constructor", func(t *testing.T) {
		maxConnTime := time.Hour
		withStreamProvider(t, maxConnTime, func(sp StreamProvider) {
			require.IsType(t, &clientSidePingStreamProvider{}, sp)
			assert.True(t, sp.(*clientSidePingStreamProvider).isJSClient)
			verifyServerProperties(t, sp.(*clientSidePingStreamProvider).server, maxConnTime)
		})
	})

	t.Run("Handler", func(t *testing.T) {
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.NotNil(t, sp.Handler(validCredential))
			assert.Nil(t, sp.Handler(invalidCredential1))
			assert.Nil(t, sp.Handler(invalidCredential2))
		})
	})

	t.Run("Register", func(t *testing.T) {
		store := makeMockStore(nil, nil, nil, nil)
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.Nil(t, sp.Register(invalidCredential1, store, ldlog.NewDisabledLoggers()))
			assert.Nil(t, sp.Register(invalidCredential2, store, ldlog.NewDisabledLoggers()))

			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()
			require.IsType(t, &clientSidePingEnvStreamProvider{}, esp)
		})
	})
}

func TestStreamProviderAllClientSidePing(t *testing.T) {
	// This uses only the mobile ping stream to test the event behavior, because we are using the same
	// implementation type for both mobile and JS client and we've already tested the individual
	// constructors above.

	validCredential := sdkauth.New(testMobileKey)
	withStreamProvider := func(t *testing.T, maxConnTime time.Duration, action func(StreamProvider)) {
		sp := NewStreamProvider(basictypes.MobilePingStream, maxConnTime)
		require.NotNil(t, sp)
		defer sp.Close()
		action(sp)
	}

	t.Run("initial event", func(t *testing.T) {
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1}, []ldmodel.ConfigOverride{testIndexSamplingOverride}, []ldmodel.Metric{testMetric1})

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, MakePingEvent())
		})
	})

	t.Run("initial event - store not initialized", func(t *testing.T) {
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1}, []ldmodel.ConfigOverride{testIndexSamplingOverride}, []ldmodel.Metric{testMetric1})
		store.initialized = false

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, nil)
		})
	})

	t.Run("SendAllDataUpdate", func(t *testing.T) {
		store := makeMockStore(nil, nil, nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			newData := []ldstoretypes.Collection{
				{Kind: ldstoreimpl.Features(), Items: store.flags},
				{Kind: ldstoreimpl.Segments(), Items: store.segments},
			}

			verifyHandlerUpdateEvent(t, sp, validCredential, MakePingEvent(),
				func() {
					esp.SendAllDataUpdate(newData)
				},
				MakePingEvent(),
			)
		})
	})

	t.Run("SendSingleItemUpdate", func(t *testing.T) {
		store := makeMockStore(nil, nil, nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerUpdateEvent(t, sp, validCredential, MakePingEvent(),
				func() {
					esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))
				},
				MakePingEvent(),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakePingEvent(),
				func() {
					esp.SendSingleItemUpdate(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.SegmentDesc(testSegment1))
				},
				MakePingEvent(),
			)
		})
	})

	t.Run("Heartbeat", func(t *testing.T) {
		store := makeMockStore(nil, nil, nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerHeartbeat(t, sp, esp, validCredential)
		})
	})
}
