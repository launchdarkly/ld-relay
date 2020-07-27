package streams

import (
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

// EnvStreamsUpdates is an interface representing the kinds of updates we can publish to streams. Other
// components that publish updates to EnvStreams should use this interface rather than the implementation
// type, both to clarify that they don't need other EnvStreams functionality and to simplify testing.
type EnvStreamsUpdates interface {
	SendAllDataUpdate(allData []ldstoretypes.Collection)
	SendSingleItemUpdate(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor)
}

// EnvStreams encapsulates streaming behavior for a specific environment. It implements the
// EnvStreamsUpdates interface.
type EnvStreams struct {
	publishers       *Publishers
	sdkKey           config.SDKKey
	mobileKey        config.MobileKey
	envID            config.EnvironmentID
	allDataRepo      *allDataStreamRepository
	flagsOnlyRepo    *flagsOnlyStreamRepository
	pingRepo         *pingStreamRepository
	sdkKeyChannels   []string
	mobileChannels   []string
	jsClientChannels []string
	heartbeats       *time.Ticker
}

// EnvStoreQueries is a subset of DataStore methods that are used by EnvStreams to query existing
// data from the store, for generating "put" events.
type EnvStoreQueries interface {
	IsInitialized() bool
	GetAll(ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error)
}

// NewEnvStreams creates an instance of EnvStreams, and registers the appropriate channels in the
// existing eventsource.Server instances that are in the Publishers.
func NewEnvStreams(
	publishers *Publishers,
	storeQueries EnvStoreQueries,
	sdkKey config.SDKKey,
	mobileKey config.MobileKey,
	envID config.EnvironmentID,
	heartbeatInterval time.Duration,
	loggers ldlog.Loggers,
) *EnvStreams {
	es := &EnvStreams{
		publishers:     publishers,
		sdkKey:         sdkKey,
		mobileKey:      mobileKey,
		envID:          envID,
		allDataRepo:    &allDataStreamRepository{store: storeQueries, loggers: loggers},
		flagsOnlyRepo:  &flagsOnlyStreamRepository{store: storeQueries, loggers: loggers},
		pingRepo:       &pingStreamRepository{},
		sdkKeyChannels: []string{string(sdkKey)},
	}

	publishers.ServerSideAll.Register(string(sdkKey), es.allDataRepo)
	publishers.ServerSideFlags.Register(string(sdkKey), es.flagsOnlyRepo)

	if mobileKey != "" {
		es.mobileChannels = []string{string(mobileKey)}
		publishers.Mobile.Register(string(mobileKey), es.pingRepo)
	}
	if envID != "" {
		es.jsClientChannels = []string{string(envID)}
		publishers.JSClient.Register(string(envID), es.pingRepo)
	}

	if heartbeatInterval > 0 {
		es.heartbeats = time.NewTicker(heartbeatInterval)
		go func() {
			for range es.heartbeats.C {
				es.sendHeartbeat()
			}
		}()
	}

	return es
}

// SendAllDataUpdate sends all appropriate stream updates for when the full data set has been refreshed.
func (es *EnvStreams) SendAllDataUpdate(
	allData []ldstoretypes.Collection,
) {
	es.publishers.ServerSideAll.Publish(es.sdkKeyChannels, MakeServerSidePutEvent(allData))
	es.publishers.ServerSideFlags.Publish(es.sdkKeyChannels, MakeServerSideFlagsOnlyPutEvent(allData))
	if len(es.mobileChannels) != 0 {
		es.publishers.Mobile.Publish(es.mobileChannels, MakePingEvent())
	}
	if len(es.jsClientChannels) != 0 {
		es.publishers.JSClient.Publish(es.jsClientChannels, MakePingEvent())
	}
}

// SendSingleItemUpdate sends all appropriate stream updates for when an individual item has been updated.
func (es *EnvStreams) SendSingleItemUpdate(
	kind ldstoretypes.DataKind,
	key string,
	item ldstoretypes.ItemDescriptor,
) {
	if item.Item == nil {
		es.publishers.ServerSideAll.Publish(es.sdkKeyChannels,
			MakeServerSideDeleteEvent(kind, key, item.Version))
		if kind == ldstoreimpl.Features() {
			es.publishers.ServerSideFlags.Publish(es.sdkKeyChannels,
				MakeServerSideFlagsOnlyDeleteEvent(key, item.Version))
		}
	} else {
		es.publishers.ServerSideAll.Publish(es.sdkKeyChannels, MakeServerSidePatchEvent(kind, key, item))
		if kind == ldstoreimpl.Features() {
			es.publishers.ServerSideFlags.Publish(es.sdkKeyChannels,
				MakeServerSideFlagsOnlyPatchEvent(key, item))
		}
	}
	if len(es.mobileChannels) != 0 {
		es.publishers.Mobile.Publish(es.mobileChannels, MakePingEvent())
	}
	if len(es.jsClientChannels) != 0 {
		es.publishers.JSClient.Publish(es.jsClientChannels, MakePingEvent())
	}
}

// Close shuts down all currently active streams for this environment and releases its resources.
func (es *EnvStreams) Close() error {
	if es.heartbeats != nil {
		es.heartbeats.Stop()
	}
	es.publishers.ServerSideAll.Unregister(string(es.sdkKey), true)
	es.publishers.ServerSideFlags.Unregister(string(es.sdkKey), true)
	if es.mobileKey != "" {
		es.publishers.Mobile.Unregister(string(es.mobileKey), true)
	}
	if es.envID != "" {
		es.publishers.JSClient.Unregister(string(es.envID), true)
	}
	return nil
}

func (es *EnvStreams) sendHeartbeat() {
	es.publishers.ServerSideAll.PublishComment(es.sdkKeyChannels, "")
	es.publishers.ServerSideFlags.PublishComment(es.sdkKeyChannels, "")
	if len(es.mobileChannels) != 0 {
		es.publishers.Mobile.PublishComment(es.mobileChannels, "")
	}
	if len(es.jsClientChannels) != 0 {
		es.publishers.JSClient.PublishComment(es.jsClientChannels, "")
	}
}
