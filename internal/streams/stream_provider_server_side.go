package streams

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/config"
	"golang.org/x/sync/singleflight"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// This is the standard implementation of the /sdk/stream (fdv2) & /all (fdv1)
// stream endpoints for server-side SDKs.
type serverSideStreamProvider struct {
	fdv1Server *eventsource.Server
	fdv2Server *eventsource.Server
	closeOnce  sync.Once
}

type serverSideEnvStreamProvider struct {
	server   *eventsource.Server
	channels []string
	isV2     bool
}

type serverSideEnvStreamRepository struct {
	store   EnvStoreQueries
	loggers ldlog.Loggers
	isV2    bool

	flightGroup singleflight.Group
}

func (s *serverSideStreamProvider) HandlerV1(credential sdkauth.ScopedCredential) http.HandlerFunc {
	if _, ok := credential.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	return s.fdv1Server.Handler(credential.String())
}

func (s *serverSideStreamProvider) HandlerV2(credential sdkauth.ScopedCredential) http.HandlerFunc {
	if _, ok := credential.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	return s.fdv2Server.Handler(credential.String())
}

func (s *serverSideStreamProvider) RegisterV1(
	credential sdkauth.ScopedCredential,
	store EnvStoreQueries,
	loggers ldlog.Loggers,
) EnvStreamProvider {
	if _, ok := credential.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	repo := &serverSideEnvStreamRepository{store: store, loggers: loggers, isV2: false}
	s.fdv1Server.Register(credential.String(), repo)
	envStream := &serverSideEnvStreamProvider{server: s.fdv1Server, channels: []string{credential.String()}, isV2: false}
	return envStream
}

func (s *serverSideStreamProvider) RegisterV2(
	credential sdkauth.ScopedCredential,
	store EnvStoreQueries,
	loggers ldlog.Loggers,
) EnvStreamProvider {
	if _, ok := credential.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	repo := &serverSideEnvStreamRepository{store: store, loggers: loggers, isV2: true}
	s.fdv2Server.Register(credential.String(), repo)
	envStream := &serverSideEnvStreamProvider{server: s.fdv2Server, channels: []string{credential.String()}, isV2: true}
	return envStream
}

func (s *serverSideStreamProvider) Close() {
	s.closeOnce.Do(func() {
		s.fdv1Server.Close()
		s.fdv2Server.Close()
	})
}

func (e *serverSideEnvStreamProvider) SetBasis(events []subsystems.Change, selector subsystems.Selector) {
	if e.isV2 {
		for _, event := range MakeEventsForSetBasis(events, selector) {
			e.server.Publish(e.channels, event)
		}
	} else {
		allData, err := subsystems.ToStorableItems(events)
		if err != nil {
			return
		}
		e.server.Publish(e.channels, MakeServerSidePutEvent(allData))
	}
}

func (e *serverSideEnvStreamProvider) ApplyDelta(events []subsystems.Change, selector subsystems.Selector) {
	if e.isV2 {
		for _, event := range MakeEventsForApplyDelta(events, selector) {
			e.server.Publish(e.channels, event)
		}
	} else {
		allData, err := subsystems.ToStorableItems(events)
		if err != nil {
			return
		}
		for _, collection := range allData {
			for _, item := range collection.Items {
				if item.Item.Item == nil {
					e.server.Publish(e.channels, MakeServerSideDeleteEvent(collection.Kind, item.Key, item.Item.Version))
				} else {
					e.server.Publish(e.channels, MakeServerSidePatchEvent(collection.Kind, item.Key, item.Item))
				}
			}
		}
	}
}

func (e *serverSideEnvStreamProvider) InvalidateClientSideState() {}

func (e *serverSideEnvStreamProvider) SendHeartbeat() {
	e.server.PublishComment(e.channels, "")
}

func (e *serverSideEnvStreamProvider) Close() {
	for _, key := range e.channels {
		e.server.Unregister(key, true)
	}
}

func (r *serverSideEnvStreamRepository) Replay(channel, id string) chan eventsource.Event {
	out := make(chan eventsource.Event)
	if !r.store.IsInitialized() {
		// If the data store has never been populated, we won't send an initial event. This is desirable
		// behavior because, if Relay is still waiting on flag data from LD, we want SDK clients to stay
		// waiting on Relay; then once Relay gets a "put" event from the LD stream, it will broadcast that
		// event to this stream.
		close(out)
		return out
	}
	go func() {
		defer close(out)
		var events []eventsource.Event
		var err error
		if r.isV2 {
			//nolint:godox
			// TODO(fdv2): Only send this if the provided selector doesn't match the
			// current store.
			events, err = r.getReplayEventsV2()
		} else {
			events, err = r.getReplayEventsV1()
		}

		if err != nil {
			return
		}
		for _, event := range events {
			out <- event
		}
	}()
	return out
}

// getReplayEvent will return a ServerSidePutEvent with all the data needed for a Replay.
func (r *serverSideEnvStreamRepository) getReplayEventsV1() ([]eventsource.Event, error) {
	data, err, _ := r.flightGroup.Do("getReplayEvent", func() (interface{}, error) {
		flags, err := r.store.GetAll(ldstoreimpl.Features())
		if err != nil {
			r.loggers.Errorf("Error getting all flags: %s\n", err.Error())
			return nil, err
		}
		segments, err := r.store.GetAll(ldstoreimpl.Segments())
		if err != nil {
			r.loggers.Errorf("Error getting all segments: %s\n", err.Error())
			return nil, err
		}

		allData := []ldstoretypes.Collection{
			{Kind: ldstoreimpl.Features(), Items: removeDeleted(flags)},
			{Kind: ldstoreimpl.Segments(), Items: removeDeleted(segments)},
		}

		event := MakeServerSidePutEvent(allData)
		return event, nil
	})

	if err != nil {
		return nil, err
	}

	// panic if it's not an eventsource.Event - as this should be impossible
	event := data.(eventsource.Event)
	return []eventsource.Event{event}, nil
}

func (r *serverSideEnvStreamRepository) getReplayEventsV2() ([]eventsource.Event, error) {
	data, err, _ := r.flightGroup.Do("getReplayEvent", func() (interface{}, error) {
		flags, err := r.store.GetAll(ldstoreimpl.Features())
		if err != nil {
			r.loggers.Errorf("Error getting all flags: %s\n", err.Error())
			return nil, err
		}
		segments, err := r.store.GetAll(ldstoreimpl.Segments())
		if err != nil {
			r.loggers.Errorf("Error getting all segments: %s\n", err.Error())
			return nil, err
		}

		changes := []subsystems.Change{}
		for _, flag := range flags {
			//nolint:godox
			// TODO(fdv2): This is a temporary implementation until we can change the
			// language to be in changesets
			flagJson, err := json.Marshal(flag.Item)
			if err != nil {
				return nil, err
			}
			changes = append(changes, subsystems.Change{
				Action:  subsystems.ChangeTypePut,
				Kind:    subsystems.FlagKind,
				Key:     flag.Key,
				Version: flag.Item.Version,
				Object:  flagJson,
			})
		}
		for _, segment := range segments {
			//nolint:godox
			// TODO(fdv2): This is a temporary implementation until we can change the
			// language to be in changesets
			segmentJson, err := json.Marshal(segment.Item)
			if err != nil {
				return nil, err
			}
			changes = append(changes, subsystems.Change{
				Action:  subsystems.ChangeTypePut,
				Kind:    subsystems.SegmentKind,
				Key:     segment.Key,
				Version: segment.Item.Version,
				Object:  segmentJson,
			})
		}

		return MakeEventsForSetBasis(changes, subsystems.Selector{}), nil
	})

	if err != nil {
		return nil, err
	}

	// panic if it's not an eventsource.Event - as this should be impossible
	return data.([]eventsource.Event), nil
}
