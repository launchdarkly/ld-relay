package store

import (
	"testing"

	"github.com/stretchr/testify/assert"

	es "github.com/launchdarkly/eventsource"
	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
)

type testPublisher struct {
	events   []es.Event
	comments []string
}

func (p *testPublisher) Publish(channels []string, event es.Event) {
	p.events = append(p.events, event)
}

func (p *testPublisher) PublishComment(channels []string, text string) {
	p.comments = append(p.comments, text)
}

func (p *testPublisher) Register(channel string, repo es.Repository) {}

func TestRelayFeatureStore(t *testing.T) {
	t.Run("init", func(t *testing.T) {
		baseStore := ld.NewInMemoryFeatureStore(nil)
		allPublisher := &testPublisher{}
		flagsPublisher := &testPublisher{}
		pingPublisher := &testPublisher{}
		store := NewSSERelayFeatureStore("api-key", allPublisher, flagsPublisher, pingPublisher, baseStore, 1)

		store.Init(nil)
		emptyDataMap := map[string]ld.VersionedData{}
		var nilDataMap map[string]ld.VersionedData
		emptyAllMap := map[string]map[string]ld.VersionedData{"flags": emptyDataMap, "segments": emptyDataMap}
		assert.EqualValues(t, []es.Event{allPutEvent{D: emptyAllMap}}, allPublisher.events)
		assert.EqualValues(t, []es.Event{flagsPutEvent(nilDataMap)}, flagsPublisher.events)
		assert.EqualValues(t, []es.Event{pingEvent{}}, pingPublisher.events)
	})

	t.Run("delete flag", func(t *testing.T) {
		baseStore := ld.NewInMemoryFeatureStore(nil)
		baseStore.Init(nil)
		allPublisher := &testPublisher{}
		flagsPublisher := &testPublisher{}
		pingPublisher := &testPublisher{}
		store := NewSSERelayFeatureStore("api-key", allPublisher, flagsPublisher, pingPublisher, baseStore, 1)

		store.Delete(ld.Features, "my-flag", 1)
		assert.EqualValues(t, []es.Event{deleteEvent{Path: "/flags/my-flag", Version: 1}}, allPublisher.events)
		assert.EqualValues(t, []es.Event{deleteEvent{Path: "/my-flag", Version: 1}}, flagsPublisher.events)
		assert.EqualValues(t, []es.Event{pingEvent{}}, pingPublisher.events)
	})

	t.Run("create flag", func(t *testing.T) {
		baseStore := ld.NewInMemoryFeatureStore(nil)
		baseStore.Init(nil)
		allPublisher := &testPublisher{}
		flagsPublisher := &testPublisher{}
		pingPublisher := &testPublisher{}
		store := NewSSERelayFeatureStore("api-key", allPublisher, flagsPublisher, pingPublisher, baseStore, 1)

		newFlag := ld.FeatureFlag{Key: "my-new-flag", Version: 1}
		store.Upsert(ld.Features, &newFlag)
		assert.EqualValues(t, []es.Event{upsertEvent{Path: "/flags/my-new-flag", D: &newFlag}}, allPublisher.events)
		assert.EqualValues(t, []es.Event{upsertEvent{Path: "/my-new-flag", D: &newFlag}}, flagsPublisher.events)
		assert.EqualValues(t, []es.Event{pingEvent{}}, pingPublisher.events)
	})

	t.Run("update flag", func(t *testing.T) {
		baseStore := ld.NewInMemoryFeatureStore(nil)
		baseStore.Init(nil)
		originalFlag := ld.FeatureFlag{Key: "my-flag", Version: 1}
		baseStore.Upsert(ld.Features, &originalFlag)

		allPublisher := &testPublisher{}
		flagsPublisher := &testPublisher{}
		pingPublisher := &testPublisher{}
		store := NewSSERelayFeatureStore("api-key", allPublisher, flagsPublisher, pingPublisher, baseStore, 1)

		updatedFlag := ld.FeatureFlag{Key: "my-flag", Version: 2}
		store.Upsert(ld.Features, &updatedFlag)
		assert.EqualValues(t, []es.Event{upsertEvent{Path: "/flags/my-flag", D: &updatedFlag}}, allPublisher.events)
		assert.EqualValues(t, []es.Event{upsertEvent{Path: "/my-flag", D: &updatedFlag}}, flagsPublisher.events)
		assert.EqualValues(t, []es.Event{pingEvent{}}, pingPublisher.events)
	})

	t.Run("updating flag with older version just sends newer version", func(t *testing.T) {
		baseStore := ld.NewInMemoryFeatureStore(nil)
		baseStore.Init(nil)
		originalFlag := ld.FeatureFlag{Key: "my-flag", Version: 2}
		baseStore.Upsert(ld.Features, &originalFlag)

		allPublisher := &testPublisher{}
		flagsPublisher := &testPublisher{}
		pingPublisher := &testPublisher{}
		store := NewSSERelayFeatureStore("api-key", allPublisher, flagsPublisher, pingPublisher, baseStore, 1)

		staleFlag := ld.FeatureFlag{Key: "my-flag", Version: 1}
		store.Upsert(ld.Features, &staleFlag)
		assert.EqualValues(t, []es.Event{
			upsertEvent{Path: "/flags/my-flag", D: &originalFlag},
		}, allPublisher.events)
		assert.EqualValues(t, []es.Event{
			upsertEvent{Path: "/my-flag", D: &originalFlag},
		}, flagsPublisher.events)
		assert.EqualValues(t, []es.Event{
			pingEvent{},
		}, pingPublisher.events)
	})

	t.Run("updating deleted flag with older version does nothing", func(t *testing.T) {
		baseStore := ld.NewInMemoryFeatureStore(nil)
		baseStore.Init(nil)
		baseStore.Delete(ld.Features, "my-flag", 2)

		allPublisher := &testPublisher{}
		flagsPublisher := &testPublisher{}
		pingPublisher := &testPublisher{}
		store := NewSSERelayFeatureStore("api-key", allPublisher, flagsPublisher, pingPublisher, baseStore, 1)

		staleFlag := ld.FeatureFlag{Key: "my-flag", Version: 1}
		store.Upsert(ld.Features, &staleFlag)
		assert.EqualValues(t, []es.Event(nil), allPublisher.events)
		assert.EqualValues(t, []es.Event(nil), flagsPublisher.events)
		assert.EqualValues(t, []es.Event(nil), pingPublisher.events)
	})
}
