package streams

import (
	"github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

// In eventsource, a Repository is the object that can provide initial "replay" events for any new
// stream connection - specifically the "put" event in our case. Each of our types of SDK streams has
// its own format for the "put" data.

type allDataStreamRepository struct {
	store   EnvStoreQueries
	loggers ldlog.Loggers
}

type flagsOnlyStreamRepository struct {
	store   EnvStoreQueries
	loggers ldlog.Loggers
}

type pingStreamRepository struct{}

func (r *allDataStreamRepository) Replay(channel, id string) (out chan eventsource.Event) {
	out = make(chan eventsource.Event)
	go func() {
		defer close(out)
		if r.store.IsInitialized() {
			flags, err := r.store.GetAll(ldstoreimpl.Features())

			if err != nil {
				r.loggers.Errorf("Error getting all flags: %s\n", err.Error())
			} else {
				segments, err := r.store.GetAll(ldstoreimpl.Segments())
				if err != nil {
					r.loggers.Errorf("Error getting all segments: %s\n", err.Error())
				} else {
					allData := []ldstoretypes.Collection{
						{Kind: ldstoreimpl.Features(), Items: flags},
						{Kind: ldstoreimpl.Segments(), Items: segments},
					}
					out <- MakeServerSidePutEvent(allData)
				}
			}
		}
	}()
	return
}

func (r *flagsOnlyStreamRepository) Replay(channel, id string) (out chan eventsource.Event) {
	out = make(chan eventsource.Event)
	go func() {
		defer close(out)
		if r.store.IsInitialized() {
			flags, err := r.store.GetAll(ldstoreimpl.Features())

			if err != nil {
				r.loggers.Errorf("Error getting all flags: %s\n", err.Error())
			} else {
				out <- MakeServerSideFlagsOnlyPutEvent(
					[]ldstoretypes.Collection{{Kind: ldstoreimpl.Features(), Items: flags}})
			}
		}
	}()
	return
}

func (r *pingStreamRepository) Replay(channel, id string) (out chan eventsource.Event) {
	out = make(chan eventsource.Event)
	go func() {
		defer close(out)
		out <- MakePingEvent()
	}()
	return
}
