package streams

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

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
		sp := NewStreamProvider(basictypes.MobilePingStream, maxConnTime, 0)
		require.NotNil(t, sp)
		defer sp.Close()
		action(sp)
	}

	t.Run("constructor", func(t *testing.T) {
		maxConnTime := time.Hour
		withStreamProvider(t, maxConnTime, func(sp StreamProvider) {
			require.IsType(t, &clientSidePingStreamProvider{}, sp)
			assert.False(t, sp.(*clientSidePingStreamProvider).isJSClient)
			verifyServerProperties(t, sp.(*clientSidePingStreamProvider).fdv1Server, maxConnTime)
			verifyServerProperties(t, sp.(*clientSidePingStreamProvider).fdv2Server, maxConnTime)
		})
	})

	t.Run("Handler", func(t *testing.T) {
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.NotNil(t, sp.HandlerV1(validCredential))
			assert.Nil(t, sp.HandlerV1(invalidCredential1))
			assert.Nil(t, sp.HandlerV1(invalidCredential2))
		})
	})

	t.Run("Register", func(t *testing.T) {
		store := makeMockStore(nil, nil)
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.Nil(t, sp.RegisterV1(invalidCredential1, store, ldlog.NewDisabledLoggers()))
			assert.Nil(t, sp.RegisterV1(invalidCredential2, store, ldlog.NewDisabledLoggers()))

			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
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
		sp := NewStreamProvider(basictypes.JSClientPingStream, maxConnTime, 0)
		require.NotNil(t, sp)
		defer sp.Close()
		action(sp)
	}

	t.Run("constructor", func(t *testing.T) {
		maxConnTime := time.Hour
		withStreamProvider(t, maxConnTime, func(sp StreamProvider) {
			require.IsType(t, &clientSidePingStreamProvider{}, sp)
			assert.True(t, sp.(*clientSidePingStreamProvider).isJSClient)
			verifyServerProperties(t, sp.(*clientSidePingStreamProvider).fdv1Server, maxConnTime)
			verifyServerProperties(t, sp.(*clientSidePingStreamProvider).fdv2Server, maxConnTime)
		})
	})

	t.Run("Handler", func(t *testing.T) {
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.NotNil(t, sp.HandlerV1(validCredential))
			assert.Nil(t, sp.HandlerV1(invalidCredential1))
			assert.Nil(t, sp.HandlerV1(invalidCredential2))
		})
	})

	t.Run("Register", func(t *testing.T) {
		store := makeMockStore(nil, nil)
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.Nil(t, sp.RegisterV1(invalidCredential1, store, ldlog.NewDisabledLoggers()))
			assert.Nil(t, sp.RegisterV1(invalidCredential2, store, ldlog.NewDisabledLoggers()))

			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
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
		sp := NewStreamProvider(basictypes.MobilePingStream, maxConnTime, 0)
		require.NotNil(t, sp)
		defer sp.Close()
		action(sp)
	}

	t.Run("initial event", func(t *testing.T) {
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, MakePingEvent())
		})
	})

	t.Run("initial event - store not initialized", func(t *testing.T) {
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})
		store.initialized = false

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, nil)
		})
	})

	t.Run("SetBasis", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			changes := []subsystems.Change{
				{Action: subsystems.ChangeTypePut, Kind: subsystems.FlagKind, Key: testFlag1.Key, Version: 1, Object: testFlag1JSON},
				{Action: subsystems.ChangeTypePut, Kind: subsystems.SegmentKind, Key: testSegment1.Key, Version: 1, Object: testSegment1JSON},
			}

			verifyHandlerUpdateEvent(t, sp, validCredential, MakePingEvent(),
				func() {
					esp.SetBasis(changes, subsystems.Selector{})
				},
				MakePingEvent(),
			)
		})
	})

	t.Run("ApplyDelta", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerUpdateEvent(t, sp, validCredential, MakePingEvent(),
				func() {
					esp.ApplyDelta([]subsystems.Change{
						{Action: subsystems.ChangeTypePut, Kind: subsystems.FlagKind, Key: testFlag1.Key, Object: testFlag1JSON},
					}, subsystems.NoSelector())
				},
				MakePingEvent(),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakePingEvent(),
				func() {
					esp.ApplyDelta([]subsystems.Change{
						{Action: subsystems.ChangeTypePut, Kind: subsystems.SegmentKind, Key: testSegment1.Key, Object: testSegment1JSON},
					}, subsystems.NoSelector())
				},
				MakePingEvent(),
			)
		})
	})

	t.Run("Heartbeat", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerHeartbeat(t, sp, esp, validCredential)
		})
	})
}
