package main

import (
	"encoding/json"
	es "github.com/launchdarkly/eventsource"
	ld "github.com/launchdarkly/go-client"
	"time"
)

type SSERelayFeatureStore struct {
	store     ld.FeatureStore
	publisher *es.Server
	apiKey    string
}

func NewSSERelayFeatureStore(apiKey string, publisher *es.Server, heartbeatInterval int) *SSERelayFeatureStore {
	relayStore := &SSERelayFeatureStore{
		store:     ld.NewInMemoryFeatureStore(),
		apiKey:    apiKey,
		publisher: publisher,
	}

	publisher.Register(apiKey, relayStore)

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

func (relay *SSERelayFeatureStore) heartbeat() {
	relay.publisher.Publish([]string{relay.apiKey}, heartbeatEvent("hb"))
}

func (relay *SSERelayFeatureStore) Get(key string) (*ld.Feature, error) {
	return relay.store.Get(key)
}

func (relay *SSERelayFeatureStore) All() (map[string]*ld.Feature, error) {
	return relay.store.All()
}

func (relay *SSERelayFeatureStore) Init(flags map[string]*ld.Feature) error {
	err := relay.store.Init(flags)

	if err != nil {
		return err
	}

	relay.publisher.Publish([]string{relay.apiKey}, makePutEvent(flags))

	return nil
}

func (relay *SSERelayFeatureStore) Delete(key string, version int) error {
	err := relay.store.Delete(key, version)
	if err != nil {
		return err
	}

	relay.publisher.Publish([]string{relay.apiKey}, makeDeleteEvent(key, version))

	return nil
}

func (relay *SSERelayFeatureStore) Upsert(key string, f ld.Feature) error {
	err := relay.store.Upsert(key, f)

	if err != nil {
		return err
	}

	flag, err := relay.store.Get(key)

	if err != nil {
		return err
	}

	if flag != nil {
		relay.publisher.Publish([]string{relay.apiKey}, makeUpsertEvent(*flag))
	}

	return nil
}

func (relay *SSERelayFeatureStore) Initialized() bool {
	return relay.store.Initialized()
}

// Allows the feature store to act as an SSE repository (to send bootstrap events)
func (relay *SSERelayFeatureStore) Replay(channel, id string) (out chan es.Event) {
	out = make(chan es.Event)
	go func() {
		defer close(out)
		if relay.Initialized() {
			flags, err := relay.All()

			if err != nil {
				Error.Printf("Error getting all flags: %s\n", err.Error())
			} else {
				out <- makePutEvent(flags)
			}
		}
	}()
	return
}

type putEvent map[string]*ld.Feature

type deleteEvent struct {
	Path    string `json:"path"`
	Version int    `json:"version"`
}

type upsertEvent struct {
	Path string     `json:"path"`
	D    ld.Feature `json:"data"`
}

type heartbeatEvent string

func (h heartbeatEvent) Id() string {
	return ""
}

func (h heartbeatEvent) Event() string {
	return ""
}

func (h heartbeatEvent) Data() string {
	return ""
}

func (h heartbeatEvent) Comment() string {
	return string(h)
}

func (t putEvent) Id() string {
	return ""
}

func (t putEvent) Event() string {
	return "put"
}

func (t putEvent) Data() string {
	data, _ := json.Marshal(t)

	return string(data)
}

func (t putEvent) Comment() string {
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

func makeUpsertEvent(f ld.Feature) es.Event {
	return upsertEvent{
		Path: "/" + *f.Key,
		D:    f,
	}
}

func makeDeleteEvent(key string, version int) es.Event {
	return deleteEvent{
		Path:    "/" + key,
		Version: version,
	}
}

func makePutEvent(flags map[string]*ld.Feature) es.Event {
	return putEvent(flags)
}
