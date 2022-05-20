package streams

import (
	"crypto/md5"
	"net/http"
	"sort"
	"strconv"
	"sync"

	"github.com/launchdarkly/ld-relay/v6/config"

	"github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

// This is the standard implementation of the /flags stream for old server-side SDKs.

type serverSideFlagsOnlyStreamProvider struct {
	server    *eventsource.Server
	closeOnce sync.Once
}

type serverSideFlagsOnlyEnvStreamProvider struct {
	server   *eventsource.Server
	channels []string
}

type serverSideFlagsOnlyEnvStreamRepository struct {
	store   EnvStoreQueries
	loggers ldlog.Loggers

	mu            sync.Mutex
	cacheEvent    eventsource.Event
	eventChecksum string
}

func (s *serverSideFlagsOnlyStreamProvider) Handler(credential config.SDKCredential) http.HandlerFunc {
	if key, ok := credential.(config.SDKKey); ok {
		return s.server.Handler(string(key))
	}
	return nil
}

func (s *serverSideFlagsOnlyStreamProvider) Register(
	credential config.SDKCredential,
	store EnvStoreQueries,
	loggers ldlog.Loggers,
) EnvStreamProvider {
	if key, ok := credential.(config.SDKKey); ok {
		repo := &serverSideFlagsOnlyEnvStreamRepository{store: store, loggers: loggers}
		s.server.Register(string(key), repo)
		envStream := &serverSideFlagsOnlyEnvStreamProvider{server: s.server, channels: []string{string(key)}}
		return envStream
	}
	return nil
}

func (s *serverSideFlagsOnlyStreamProvider) Close() {
	s.closeOnce.Do(func() {
		s.server.Close()
	})
}

func (e *serverSideFlagsOnlyEnvStreamProvider) SendAllDataUpdate(allData []ldstoretypes.Collection) {
	e.server.Publish(e.channels, MakeServerSideFlagsOnlyPutEvent(allData))
}

func (e *serverSideFlagsOnlyEnvStreamProvider) SendSingleItemUpdate(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) {
	if kind != ldstoreimpl.Features() {
		return
	}
	if item.Item == nil {
		e.server.Publish(e.channels, MakeServerSideFlagsOnlyDeleteEvent(key, item.Version))
	} else {
		e.server.Publish(e.channels, MakeServerSideFlagsOnlyPatchEvent(key, item))
	}
}

func (e *serverSideFlagsOnlyEnvStreamProvider) InvalidateClientSideState() {}

func (e *serverSideFlagsOnlyEnvStreamProvider) SendHeartbeat() {
	e.server.PublishComment(e.channels, "")
}

func (e *serverSideFlagsOnlyEnvStreamProvider) Close() {
	for _, key := range e.channels {
		e.server.Unregister(key, true)
	}
}

func (r *serverSideFlagsOnlyEnvStreamRepository) Replay(channel, id string) chan eventsource.Event {
	out := make(chan eventsource.Event)
	if !r.store.IsInitialized() { // See serverSideEnvStreamRepository.Replay
		close(out)
		return out
	}
	go func() {
		defer close(out)
		if !r.store.IsInitialized() {
			return
		}
		flags, err := r.store.GetAll(ldstoreimpl.Features())

		if err != nil {
			r.loggers.Errorf("Error getting all flags: %s\n", err.Error())
			return
		}

		checksum := hashKeyedItemDescriptors(flags)
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.eventChecksum == checksum {
			out <- r.cacheEvent
			return
		}

		event := MakeServerSideFlagsOnlyPutEvent(
			[]ldstoretypes.Collection{{Kind: ldstoreimpl.Features(), Items: removeDeleted(flags)}})
		r.eventChecksum = checksum
		r.cacheEvent = event

		out <- event
	}()
	return out
}

type keyVersion struct {
	key     string
	version int
}

func hashKeyedItemDescriptors(keyedItems []ldstoretypes.KeyedItemDescriptor) string {
	kvs := make([]keyVersion, len(keyedItems))
	for i, ki := range keyedItems {
		kvs[i] = keyVersion{ki.Key, ki.Item.Version}
	}

	// We sort because the order of the data we get may not be consistent.
	sort.Slice(kvs, func(a, b int) bool { return kvs[a].key < kvs[b].key })

	h := md5.New()
	for _, kv := range kvs {
		h.Write([]byte(kv.key))
		h.Write([]byte("#")) // something that won't be part of a key
		h.Write([]byte(strconv.Itoa(kv.version)))
		h.Write([]byte(";")) // something that won't be part of a version
	}
	checksum := string(h.Sum(nil))
	return checksum
}
