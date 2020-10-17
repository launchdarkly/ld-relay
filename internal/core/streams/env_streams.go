package streams

import (
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
)

// EnvStreamUpdates is an interface representing the kinds of updates we can publish to streams. Other
// components that publish updates to EnvStreams should use this interface rather than the implementation
// type, both to clarify that they don't need other EnvStreams functionality and to simplify testing.
type EnvStreamUpdates interface {
	SendAllDataUpdate(allData []ldstoretypes.Collection)
	SendSingleItemUpdate(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor)
}

// EnvStreams encapsulates streaming behavior for a specific environment.
//
// EnvStreams itself does not know anything about what kind of streams are available; those are
// determined by the StreamProvider instances that are passed in the constructor, and the credentials
// that are registered with AddCredential. For each combination of a credential and a StreamProvider
// that can handle that credential, a stream is available, and data updates that are sent with the
// EnvStreamUpdates methods will be rebroadcast to all of those streams, in a format that is
// determined by each StreamProvider.
type EnvStreams struct {
	streamProviders []StreamProvider
	storeQueries    EnvStoreQueries
	activeStreams   []streamInfo
	loggers         ldlog.Loggers
	lock            sync.RWMutex
	closeCh         chan struct{}
}

type streamInfo struct {
	credential        config.SDKCredential
	envStreamProvider EnvStreamProvider
}

// EnvStoreQueries is a subset of DataStore methods that are used by EnvStreams to query existing
// data from the store, for generating "put" events.
type EnvStoreQueries interface {
	IsInitialized() bool
	GetAll(ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error)
}

// NewEnvStreams creates an instance of EnvStreams.
func NewEnvStreams(
	streamProviders []StreamProvider,
	storeQueries EnvStoreQueries,
	heartbeatInterval time.Duration,
	loggers ldlog.Loggers,
) *EnvStreams {
	es := &EnvStreams{
		streamProviders: streamProviders,
		storeQueries:    storeQueries,
		loggers:         loggers,
		closeCh:         make(chan struct{}),
	}

	if heartbeatInterval > 0 {
		heartbeats := time.NewTicker(heartbeatInterval)
		go func() {
			for {
				select {
				case <-heartbeats.C:
					for _, esp := range es.getEnvStreamProviders() {
						esp.SendHeartbeat()
					}
				case <-es.closeCh:
					heartbeats.Stop()
					return
				}
			}
		}()
	}

	return es
}

// AddCredential adds an environment credential and creates a corresponding EnvStreamProvider.
func (es *EnvStreams) AddCredential(credential config.SDKCredential) {
	if credential == nil {
		return
	}
	for _, sp := range es.streamProviders {
		if esp := sp.Register(credential, es.storeQueries, es.loggers); esp != nil {
			es.lock.Lock()
			es.activeStreams = append(es.activeStreams, streamInfo{credential, esp})
			es.lock.Unlock()
		}
	}
}

// RemoveCredential shuts down the EnvStreamProvider, if any, for the specified credential.
func (es *EnvStreams) RemoveCredential(credential config.SDKCredential) {
	var retained []streamInfo
	var removed []EnvStreamProvider

	es.lock.Lock()
	for _, s := range es.activeStreams {
		if s.credential == credential {
			removed = append(removed, s.envStreamProvider)
		} else {
			retained = append(retained, s)
		}
	}
	es.activeStreams = retained
	es.lock.Unlock()

	for _, s := range removed {
		s.Close()
	}
}

// SendAllDataUpdate sends all appropriate stream updates for when the full data set has been refreshed.
func (es *EnvStreams) SendAllDataUpdate(
	allData []ldstoretypes.Collection,
) {
	for _, esp := range es.getEnvStreamProviders() {
		esp.SendAllDataUpdate(allData)
	}
}

// SendSingleItemUpdate sends all appropriate stream updates for when an individual item has been updated.
func (es *EnvStreams) SendSingleItemUpdate(
	kind ldstoretypes.DataKind,
	key string,
	item ldstoretypes.ItemDescriptor,
) {
	for _, esp := range es.getEnvStreamProviders() {
		esp.SendSingleItemUpdate(kind, key, item)
	}
}

// Close shuts down all currently active streams for this environment and releases its resources.
func (es *EnvStreams) Close() error {
	close(es.closeCh)
	for _, esp := range es.getEnvStreamProviders() {
		esp.Close()
	}
	return nil
}

func (es *EnvStreams) getEnvStreamProviders() []EnvStreamProvider {
	es.lock.RLock()
	ret := make([]EnvStreamProvider, 0, len(es.activeStreams))
	for _, s := range es.activeStreams {
		ret = append(ret, s.envStreamProvider)
	}
	es.lock.RUnlock()
	return ret
}
