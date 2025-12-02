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

			changeSet, err := subsystems.NewChangeSetBuilder().
				Start(subsystems.ServerIntent{
					Payload: subsystems.Payload{
						ID:     "state",
						Target: 1,
						Code:   subsystems.IntentTransferFull,
						Reason: "cant-catchup",
					},
				}).
				AddPut(subsystems.FlagKind, testFlag1.Key, 1, testFlag1JSON).
				AddPut(subsystems.SegmentKind, testSegment1.Key, 1, testSegment1JSON).
				Finish(subsystems.NewSelector("state", 1))
			require.NoError(t, err)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakePingEvent(),
				func() {
					esp.Apply(*changeSet)
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

			changeSetBuilder := subsystems.NewChangeSetBuilder()

			changeSet, err := changeSetBuilder.
				Start(subsystems.ServerIntent{
					Payload: subsystems.Payload{
						ID:     "state",
						Target: 1,
						Code:   subsystems.IntentTransferChanges,
						Reason: "stale",
					},
				}).
				AddPut(subsystems.FlagKind, testFlag1.Key, 1, testFlag1JSON).
				Finish(subsystems.NewSelector("state", 1))
			assert.NoError(t, err)

			assert.NoError(t, changeSetBuilder.ExpectChanges())

			verifyHandlerUpdateEvent(t, sp, validCredential, MakePingEvent(),
				func() { esp.Apply(*changeSet) },
				MakePingEvent(),
			)

			segmentChangeSet, err := changeSetBuilder.
				AddPut(subsystems.SegmentKind, testSegment1.Key, 1, testSegment1JSON).
				Finish(subsystems.NewSelector("state", 2))
			assert.NoError(t, err)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakePingEvent(),
				func() { esp.Apply(*segmentChangeSet) },
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
