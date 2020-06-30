package store

import (
	"encoding/json"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/ld-relay.v6/logging"

	es "github.com/launchdarkly/eventsource"
)

// ESPublisher defines an interface for publishing events to eventsource
type ESPublisher interface {
	Publish(channels []string, event es.Event)
	PublishComment(channels []string, text string)
	Register(channel string, repo es.Repository)
}

// SSERelayDataStoreAdapter is used to create the SSERelayDataStore.
//
// Because the SDK normally wants to manage the lifecycle of its components, it requires you to provide
// a factory for any custom component, rather than an instance of the component itself. Then it asks the
// factory to create the instance when the LDClient is created. However, in this case we want to be able
// to access the instance externally.
//
// Also, since SSERelayDataStore is a wrapper for an underlying data store that could be a database, we
// need to be able to specify which data store implementation is being used - also as a factory.
//
// So, this factory implementation - which should only be used for a single client at a time - calls the
// wrapped factory to produce the underlying data store, then creates our own store instance, and then
// puts a reference to that instance inside itself where we can see it.
type SSERelayDataStoreAdapter struct {
	Store          interfaces.DataStore
	wrappedFactory interfaces.DataStoreFactory
	params         SSERelayDataStoreParams
}

func NewSSERelayDataStoreAdapter(
	wrappedFactory interfaces.DataStoreFactory,
	params SSERelayDataStoreParams,
) *SSERelayDataStoreAdapter {
	return &SSERelayDataStoreAdapter{wrappedFactory: wrappedFactory, params: params}
}

func (a *SSERelayDataStoreAdapter) CreateDataStore(
	context interfaces.ClientContext,
	dataStoreUpdates interfaces.DataStoreUpdates,
) (interfaces.DataStore, error) {
	wrappedStore, err := a.wrappedFactory.CreateDataStore(context, dataStoreUpdates)
	if err != nil {
		return nil, err // this will cause client initialization to fail immediately
	}
	a.Store = NewSSERelayFeatureStore(
		a.params.SDKKey,
		a.params.AllPublisher,
		a.params.FlagsPublisher,
		a.params.PingPublisher,
		wrappedStore,
		context.GetLogging().GetLoggers(),
		a.params.HeartbeatInterval,
	)
	return a.Store, nil
}

type SSERelayDataStoreParams struct {
	SDKKey            string
	AllPublisher      ESPublisher
	FlagsPublisher    ESPublisher
	PingPublisher     ESPublisher
	HeartbeatInterval int
}

type SSERelayFeatureStore struct {
	store          interfaces.DataStore
	allPublisher   ESPublisher
	flagsPublisher ESPublisher
	pingPublisher  ESPublisher
	apiKey         string
	loggers        ldlog.Loggers
}

type allRepository struct {
	relayStore *SSERelayFeatureStore
}
type flagsRepository struct {
	relayStore *SSERelayFeatureStore
}
type pingRepository struct {
	relayStore *SSERelayFeatureStore
}

// NewSSERelayFeatureStore creates a new feature store that relays different kinds of updates
func NewSSERelayFeatureStore(
	apiKey string,
	allPublisher ESPublisher,
	flagsPublisher ESPublisher,
	pingPublisher ESPublisher,
	baseFeatureStore interfaces.DataStore,
	loggers ldlog.Loggers,
	heartbeatInterval int,
) *SSERelayFeatureStore {
	relayStore := &SSERelayFeatureStore{
		store:          baseFeatureStore,
		apiKey:         apiKey,
		allPublisher:   allPublisher,
		flagsPublisher: flagsPublisher,
		pingPublisher:  pingPublisher,
		loggers:        loggers,
	}

	allPublisher.Register(apiKey, allRepository{relayStore: relayStore})
	flagsPublisher.Register(apiKey, flagsRepository{relayStore: relayStore})
	pingPublisher.Register(apiKey, pingRepository{relayStore: relayStore})

	if heartbeatInterval > 0 {
		go func() {
			t := time.NewTicker(time.Duration(heartbeatInterval) * time.Second)
			for {
				relayStore.heartbeat()
				<-t.C
			}
		}()
	}

	return relayStore
}

func (relay *SSERelayFeatureStore) keys() []string {
	return []string{relay.apiKey}
}

func (relay *SSERelayFeatureStore) heartbeat() {
	relay.allPublisher.PublishComment(relay.keys(), "")
	relay.flagsPublisher.PublishComment(relay.keys(), "")
	relay.pingPublisher.PublishComment(relay.keys(), "")
}

func (relay *SSERelayFeatureStore) Close() error {
	return relay.store.Close()
}

func (relay *SSERelayFeatureStore) IsStatusMonitoringEnabled() bool {
	return relay.store.IsStatusMonitoringEnabled()
}

// Get returns a single item from the feature store
func (relay *SSERelayFeatureStore) Get(kind interfaces.StoreDataKind, key string) (interfaces.StoreItemDescriptor, error) {
	return relay.store.Get(kind, key)
}

// All returns all items in the feature store
func (relay *SSERelayFeatureStore) GetAll(kind interfaces.StoreDataKind) ([]interfaces.StoreKeyedItemDescriptor, error) {
	return relay.store.GetAll(kind)
}

// Init initializes the feature store
func (relay *SSERelayFeatureStore) Init(allData []interfaces.StoreCollection) error {
	relay.loggers.Debug("Received all feature flags")
	err := relay.store.Init(allData)

	if err != nil {
		return err
	}

	relay.allPublisher.Publish(relay.keys(), makePutEvent(allData))
	relay.flagsPublisher.Publish(relay.keys(), makeFlagsPutEvent(getFlagsData(allData)))
	relay.pingPublisher.Publish(relay.keys(), makePingEvent())

	return nil
}

func getFlagsData(allData []interfaces.StoreCollection) []interfaces.StoreKeyedItemDescriptor {
	for _, coll := range allData {
		if coll.Kind == interfaces.DataKindFeatures() {
			return coll.Items
		}
	}
	return nil
}

// Upsert inserts or updates a single item in the feature store
func (relay *SSERelayFeatureStore) Upsert(
	kind interfaces.StoreDataKind,
	key string,
	item interfaces.StoreItemDescriptor,
) (bool, error) {
	relay.loggers.Debugf(`Received feature flag update: %s (version %d)`, key, item.Version)
	updated, err := relay.store.Upsert(kind, key, item)

	if err != nil {
		return false, err
	}

	if updated {
		if item.Item == nil {
			relay.allPublisher.Publish(relay.keys(), makeDeleteEvent(kind, key, item.Version))
			if kind == interfaces.DataKindFeatures() {
				relay.flagsPublisher.Publish(relay.keys(), makeFlagsDeleteEvent(key, item.Version))
			}
		} else {
			relay.allPublisher.Publish(relay.keys(), makeUpsertEvent(kind, key, item))
			if kind == interfaces.DataKindFeatures() {
				relay.flagsPublisher.Publish(relay.keys(), makeFlagsUpsertEvent(key, item))
			}
		}
		relay.pingPublisher.Publish(relay.keys(), makePingEvent())
	}

	return updated, nil
}

// IsInitialized returns true after the feature store has been initialized the first time
func (relay *SSERelayFeatureStore) IsInitialized() bool {
	return relay.store.IsInitialized()
}

// Replay allows the feature store to act as an SSE repository (to send bootstrap events)
func (r flagsRepository) Replay(channel, id string) (out chan es.Event) {
	out = make(chan es.Event)
	go func() {
		defer close(out)
		if r.relayStore.IsInitialized() {
			flags, err := r.relayStore.GetAll(interfaces.DataKindFeatures())

			if err != nil {
				logging.GlobalLoggers.Errorf("Error getting all flags: %s\n", err.Error())
			} else {
				out <- makeFlagsPutEvent(flags)
			}
		}
	}()
	return
}

// Replay allows the feature store to act as an SSE repository (to send bootstrap events)
func (r allRepository) Replay(channel, id string) (out chan es.Event) {
	out = make(chan es.Event)
	go func() {
		defer close(out)
		if r.relayStore.IsInitialized() {
			flags, err := r.relayStore.GetAll(interfaces.DataKindFeatures())

			if err != nil {
				logging.GlobalLoggers.Errorf("Error getting all flags: %s\n", err.Error())
			} else {
				segments, err := r.relayStore.GetAll(interfaces.DataKindSegments())
				if err != nil {
					logging.GlobalLoggers.Errorf("Error getting all segments: %s\n", err.Error())
				} else {
					allData := []interfaces.StoreCollection{
						{Kind: interfaces.DataKindFeatures(), Items: flags},
						{Kind: interfaces.DataKindSegments(), Items: segments},
					}
					out <- makePutEvent(allData)
				}
			}

		}
	}()
	return
}

// Replay allows the feature store to act as an SSE repository (to send bootstrap events)
func (r pingRepository) Replay(channel, id string) (out chan es.Event) {
	out = make(chan es.Event)
	go func() {
		defer close(out)
		out <- makePingEvent()
	}()
	return
}

var dataKindApiName = map[interfaces.StoreDataKind]string{
	interfaces.DataKindFeatures(): "flags",
	interfaces.DataKindSegments(): "segments",
}

type flagsPutEvent map[string]interface{}
type allPutEvent struct {
	D map[string]map[string]interface{} `json:"data"`
}
type deleteEvent struct {
	Path    string `json:"path"`
	Version int    `json:"version"`
}

type upsertEvent struct {
	Path string      `json:"path"`
	D    interface{} `json:"data"`
}

type pingEvent struct{}

func (t flagsPutEvent) Id() string {
	return ""
}

func (t flagsPutEvent) Event() string {
	return "put"
}

func (t flagsPutEvent) Data() string {
	data, _ := json.Marshal(t)

	return string(data)
}

func (t flagsPutEvent) Comment() string {
	return ""
}

func (t allPutEvent) Id() string {
	return ""
}

func (t allPutEvent) Event() string {
	return "put"
}

func (t allPutEvent) Data() string {
	data, _ := json.Marshal(t)

	return string(data)
}

func (t allPutEvent) Comment() string {
	return ""
}

func (t upsertEvent) Id() string {
	return ""
}

func (t upsertEvent) Event() string {
	return "patch"
}

func (t upsertEvent) Data() string {
	data, _ := json.Marshal(t)

	return string(data)
}

func (t upsertEvent) Comment() string {
	return ""
}

func (t deleteEvent) Id() string {
	return ""
}

func (t deleteEvent) Event() string {
	return "delete"
}

func (t deleteEvent) Data() string {
	data, _ := json.Marshal(t)

	return string(data)
}

func (t deleteEvent) Comment() string {
	return ""
}

func (t pingEvent) Id() string {
	return ""
}

func (t pingEvent) Event() string {
	return "ping"
}

func (t pingEvent) Data() string {
	return " " // We need something or the data field is not published by eventsource causing the event to be ignored
}

func (t pingEvent) Comment() string {
	return ""
}

func makeUpsertEvent(kind interfaces.StoreDataKind, key string, item interfaces.StoreItemDescriptor) es.Event {
	return upsertEvent{
		Path: "/" + dataKindApiName[kind] + "/" + key,
		D:    item.Item,
	}
}

func makeFlagsUpsertEvent(key string, item interfaces.StoreItemDescriptor) es.Event {
	return upsertEvent{
		Path: "/" + key,
		D:    item.Item,
	}
}

func makeDeleteEvent(kind interfaces.StoreDataKind, key string, version int) es.Event {
	return deleteEvent{
		Path:    "/" + dataKindApiName[kind] + "/" + key,
		Version: version,
	}
}

func makeFlagsDeleteEvent(key string, version int) es.Event {
	return deleteEvent{
		Path:    "/" + key,
		Version: version,
	}
}

func makePutEvent(allData []interfaces.StoreCollection) es.Event {
	var allDataMap = map[string]map[string]interface{}{
		"flags":    {},
		"segments": {},
	}
	for _, coll := range allData {
		name := dataKindApiName[coll.Kind]
		for _, item := range coll.Items {
			allDataMap[name][item.Key] = item.Item.Item
		}
	}
	return allPutEvent{D: allDataMap}
}

func makeFlagsPutEvent(flags []interfaces.StoreKeyedItemDescriptor) es.Event {
	flagsMap := make(map[string]interface{}, len(flags))
	for _, f := range flags {
		flagsMap[f.Key] = f.Item.Item
	}
	return flagsPutEvent(flagsMap)
}

func makePingEvent() es.Event {
	return pingEvent{}
}
