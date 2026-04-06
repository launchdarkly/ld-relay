package streams

import (
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v9/internal/credential"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// EnvStreamUpdates is an interface representing the kinds of updates we can publish to streams. Other
// components that publish updates to EnvStreams should use this interface rather than the implementation
// type, both to clarify that they don't need other EnvStreams functionality and to simplify testing.
type EnvStreamUpdates interface {
	// Apply accepts a changeSet containing data store modifications (such as flag or segment updates)
	// and forwards it to all active streams for broadcasting to connected SDK clients. The changeSet
	// represents atomic changes to be applied to the data store and will be serialized appropriately
	// for each stream type (server-side vs client-side SDKs).
	Apply(changeSet subsystems.ChangeSet)

	// InvalidateClientSideState signals an update that could affect client-side evaluations, but does
	// not change any server-side data item. Updating big segment data is such an event, since the
	// segment in the regular server-side SDK data does not change but evaluating the segment could
	// now produce a different result. Nothing is broadcast to the server-side SDKs (since they have
	// their own mechanisms for detecting big segment updates), but all connected client-side SDKs will
	// receive a "ping" event to refresh their state.
	InvalidateClientSideState()
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
	heartbeatsDone  chan struct{} // used in testing only
	filterKey       config.FilterKey
}

type streamInfo struct {
	credential        sdkauth.ScopedCredential
	envStreamProvider EnvStreamProvider
}

// EnvStoreQueries is a subset of DataStore methods that are used by EnvStreams to query existing
// data from the store, for generating "put" events.
type EnvStoreQueries interface {
	IsInitialized() bool
	Snapshot() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error)
}

// NewEnvStreams creates an instance of EnvStreams.
func NewEnvStreams(
	streamProviders []StreamProvider,
	storeQueries EnvStoreQueries,
	heartbeatInterval time.Duration,
	filterKey config.FilterKey,
	loggers ldlog.Loggers,
) *EnvStreams {
	es := &EnvStreams{
		streamProviders: streamProviders,
		storeQueries:    storeQueries,
		loggers:         loggers,
		closeCh:         make(chan struct{}),
		filterKey:       filterKey,
	}

	if heartbeatInterval > 0 {
		heartbeats := time.NewTicker(heartbeatInterval)
		es.heartbeatsDone = make(chan struct{})
		go func() {
			for {
				select {
				case <-heartbeats.C:
					for _, esp := range es.getEnvStreamProviders() {
						esp.SendHeartbeat()
					}
				case <-es.closeCh:
					heartbeats.Stop()
					close(es.heartbeatsDone)
					return
				}
			}
		}()
	}

	return es
}

// AddCredential adds an environment keyed off the combination of credential and payload filter,
// and creates a corresponding EnvStreamProvider.
func (es *EnvStreams) AddCredential(credential credential.SDKCredential) {
	if credential == nil {
		return
	}
	scopedCred := sdkauth.NewScoped(es.filterKey, credential)
	for _, sp := range es.streamProviders {
		if esp := sp.RegisterV1(scopedCred, es.storeQueries, es.loggers); esp != nil {
			es.lock.Lock()
			es.activeStreams = append(es.activeStreams, streamInfo{scopedCred, esp})
			es.lock.Unlock()
		}
		if esp := sp.RegisterV2(scopedCred, es.storeQueries, es.loggers); esp != nil {
			es.lock.Lock()
			es.activeStreams = append(es.activeStreams, streamInfo{scopedCred, esp})
			es.lock.Unlock()
		}
	}
}

// RemoveCredential shuts down the EnvStreamProvider, if any, specified by a combination of credential and payload filter key.
func (es *EnvStreams) RemoveCredential(credential credential.SDKCredential) {
	var retained []streamInfo
	var removed []EnvStreamProvider

	scopedCred := sdkauth.NewScoped(es.filterKey, credential)

	es.lock.Lock()
	for _, s := range es.activeStreams {
		if s.credential == scopedCred {
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

func (es *EnvStreams) Apply(changeSet subsystems.ChangeSet) {
	for _, esp := range es.getEnvStreamProviders() {
		esp.Apply(changeSet)
	}
}

// InvalidateClientSideState sends all appropriate stream updates for when client-side state should be refreshed.
func (es *EnvStreams) InvalidateClientSideState() {
	for _, esp := range es.getEnvStreamProviders() {
		esp.InvalidateClientSideState()
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
