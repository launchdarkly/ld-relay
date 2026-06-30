package autoconfig

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"sync"
	"time"

	es "github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
)

const (
	autoConfigStreamPath     = "/relay_auto_config"
	protocolVersionParam     = "rpacProtocolVersion"
	streamReadTimeout        = 5 * time.Minute // the LaunchDarkly stream should send a heartbeat comment every 3 minutes
	streamMaxRetryDelay      = 30 * time.Second
	streamRetryResetInterval = 60 * time.Second
	streamJitterRatio        = 0.5
	defaultStreamRetryDelay  = 1 * time.Second
)

var (
	// These regexes are used for obfuscating keys in debug logging
	sdkKeyJSONRegex = regexp.MustCompile(`"value": *"[^"]*([^"][^"][^"][^"])"`)
	mobKeyJSONRegex = regexp.MustCompile(`"mobKey": *"[^"]*([^"][^"][^"][^"])"`)
)

// CacheKind identifies the type of item being cached.
type CacheKind int

const (
	CacheKindEnvironment CacheKind = iota
	CacheKindFilter
)

// Cache provides read/write access to the AutoConfig persistent cache.
// This is satisfied by autoconfigcache.Store (and its noopStore) but defined here to avoid import cycles.
// Implementations manage their own context lifecycle; the caller's context is combined with the store's
// internal context so that either cancellation terminates the operation.
type Cache interface {
	io.Closer
	// GetAll returns the cached PutContent, or nil if the cache is empty.
	GetAll(ctx context.Context) (*PutContent, error)
	// SetAll writes the full PutContent to the cache, removing stale items.
	SetAll(ctx context.Context, content PutContent) error
	// Upsert writes a single item to the cache.
	Upsert(ctx context.Context, kind CacheKind, id string, data interface{}) error
	// Delete removes a single item from the cache.
	Delete(ctx context.Context, kind CacheKind, id string) error
}

// StreamManager manages the auto-configuration SSE stream.
//
// That includes managing the stream connection itself (reconnecting as needed, the same as the SDK streams),
// and also maintaining the last known state of information received from the stream so that it can determine
// whether an update is really an update (that is, checking version numbers and diffing the contents of a
// "put" event against the previous state).
//
// Relay provides an implementation of the MessageHandler interface which will be called for all changes that
// it needs to know about.
type StreamManager struct {
	key               config.AutoConfigKey
	uri               *url.URL
	handler           MessageHandler
	cache             Cache
	lastKnownEnvs     map[config.EnvironmentID]envfactory.EnvironmentRep
	httpConfig        httpconfig.HTTPConfig
	initialRetryDelay time.Duration
	loggers           ldlog.Loggers
	halt              chan struct{}
	done              chan struct{} // closed when the subscribe goroutine exits
	closeOnce         sync.Once

	// cacheCh receives the result of the async cache read started by Start().
	// It is consumed by consumeStream and nilled out after use.
	cacheCh     <-chan *PutContent
	cacheCancel context.CancelFunc

	envReceiver    *MessageReceiver[envfactory.EnvironmentRep]
	filterReceiver *MessageReceiver[envfactory.FilterRep]
}

// NewStreamManager creates a StreamManager, but does not start the connection.
// The cache is used to read cached PutContent on startup and persist it after each PUT event.
func NewStreamManager(
	key config.AutoConfigKey,
	streamURI *url.URL,
	handler MessageHandler,
	httpConfig httpconfig.HTTPConfig,
	initialRetryDelay time.Duration,
	protocolVersion int,
	loggers ldlog.Loggers,
	cache Cache,
) *StreamManager {
	loggers.SetPrefix("AutoConfiguration")
	if protocolVersion > 1 {
		streamURI.RawQuery = url.Values{
			protocolVersionParam: []string{strconv.Itoa(protocolVersion)},
		}.Encode()
	}
	s := &StreamManager{
		key:               key,
		uri:               streamURI,
		handler:           handler,
		cache:             cache,
		lastKnownEnvs:     make(map[config.EnvironmentID]envfactory.EnvironmentRep),
		httpConfig:        httpConfig,
		initialRetryDelay: initialRetryDelay,
		loggers:           loggers,
		halt:              make(chan struct{}),
	}

	// Enforces ordering constraints on the SSE messages that are sent from the server, allowing the MessageHandler
	// to act only on state changes. This process is important for mitigating unnecessary or
	// incorrect disruptions to connected SDKs. For example, modifying an environment config that *could* be done without
	// recreating an environment *should* be done without recreating that environment.
	s.envReceiver = NewMessageReceiver[envfactory.EnvironmentRep](loggers)
	s.filterReceiver = NewMessageReceiver[envfactory.FilterRep](loggers)

	// The data flow is:
	//
	// SSE message ->
	//   s.envReceiver (enforce ordering constraints) ->
	//       handler (actually create/update/delete environments)

	return s
}

// Start causes the StreamManager to start trying to connect to the auto-config stream. The returned channel
// receives nil for a successful connection, or an error if it has permanently failed.
//
// The cache read and stream connection are started concurrently. If the cache returns first,
// its data is applied so Relay can serve immediately. If the stream's first PUT arrives first,
// the cache read is cancelled and its result discarded.
func (s *StreamManager) Start() <-chan error {
	// Start the cache read concurrently with the stream connection.
	cacheCtx, cacheCancel := context.WithCancel(context.Background())
	cacheCh := make(chan *PutContent, 1)
	go func() {
		defer close(cacheCh)
		content, err := s.cache.GetAll(cacheCtx)
		if err != nil {
			if cacheCtx.Err() == nil {
				s.loggers.Warnf("AutoConfig cache read failed (will rely on stream): %v", err)
			}
			return
		}
		if content != nil {
			cacheCh <- content
		}
	}()
	s.cacheCh = cacheCh
	s.cacheCancel = cacheCancel

	s.done = make(chan struct{})
	readyCh := make(chan error, 1)
	go func() {
		defer close(s.done)
		s.subscribe(readyCh)
	}()
	return readyCh
}

// Close permanently shuts down the stream and waits for the subscribe goroutine
// to exit before closing the cache, ensuring no in-flight cache writes are interrupted.
// Safe to call even if Start() was never called.
func (s *StreamManager) Close() {
	s.closeOnce.Do(func() {
		close(s.halt)
	})
	if s.done != nil {
		<-s.done
	}
	_ = s.cache.Close()
}

type streamResult struct {
	stream *es.Stream
	err    error
}

func (s *StreamManager) subscribe(readyCh chan<- error) {
	// Ensure the cache goroutine is cancelled if subscribe exits for any reason
	// (URL error, permanent stream error, etc.) without entering consumeStream.
	defer func() {
		if s.cacheCancel != nil {
			s.cacheCancel()
			s.cacheCancel = nil
			s.cacheCh = nil
		}
	}()

	var readyOnce sync.Once
	signalReady := func(err error) { readyOnce.Do(func() { readyCh <- err }) }

	errorHandler := func(err error) es.StreamErrorHandlerResult {
		// If Close() has been called, stop retrying so the SSE goroutine can exit.
		select {
		case <-s.halt:
			return es.StreamErrorHandlerResult{CloseNow: true}
		default:
		}

		if se, ok := err.(es.SubscriptionError); ok {
			if se.Code == 401 || se.Code == 403 {
				s.loggers.Error(logMsgBadKey)
				signalReady(errors.New("invalid auto-configuration key"))
				return es.StreamErrorHandlerResult{CloseNow: true}
			}
			s.loggers.Warnf(logMsgStreamHTTPError, se.Code)
			return es.StreamErrorHandlerResult{CloseNow: false}
		}

		s.loggers.Warnf(logMsgStreamOtherError, err)
		return es.StreamErrorHandlerResult{CloseNow: false}
	}

	retry := s.initialRetryDelay
	if retry <= 0 {
		retry = defaultStreamRetryDelay // COVERAGE: never happens in unit tests
	}

	rpacEndpoint, err := url.JoinPath(s.uri.String(), autoConfigStreamPath)
	if err != nil {
		s.loggers.Errorf(logMsgBadURL, err)
		signalReady(err)
		return
	}

	req, _ := http.NewRequest("GET", rpacEndpoint, nil)
	req.Header.Set("Authorization", string(s.key))
	s.loggers.Infof(logMsgStreamConnecting, rpacEndpoint)

	// Client.Timeout must be zeroed out for stream connections, since it's not just a connect timeout
	// but a timeout for the entire response
	client := s.httpConfig.Client()
	client.Timeout = 0

	// Launch the SSE connection in a sub-goroutine so we can race it against the cache.
	streamCh := make(chan streamResult, 1)
	go func() {
		stream, err := es.SubscribeWithRequestAndOptions(req,
			es.StreamOptionHTTPClient(client),
			es.StreamOptionReadTimeout(streamReadTimeout),
			es.StreamOptionInitialRetry(retry),
			es.StreamOptionUseBackoff(streamMaxRetryDelay),
			es.StreamOptionUseJitter(streamJitterRatio),
			es.StreamOptionRetryResetInterval(streamRetryResetInterval),
			es.StreamOptionErrorHandler(errorHandler),
			es.StreamOptionCanRetryFirstConnection(-1),
			es.StreamOptionLogger(s.loggers.ForLevel(ldlog.Info)),
		)
		streamCh <- streamResult{stream, err}
	}()

	// Race the cache read against the stream connection. If the cache returns before
	// the stream's first PUT, its data is applied so Relay can serve immediately.
	// The cache is only cancelled when a PUT arrives with authoritative data.
	var stream *es.Stream
	for stream == nil {
		select {
		case content, ok := <-s.cacheCh:
			if ok && content != nil {
				s.applyCachedContent(content)
			}
			if s.cacheCancel != nil {
				s.cacheCancel()
				s.cacheCancel = nil
			}
			s.cacheCh = nil

		case result := <-streamCh:
			if result.err != nil {
				s.loggers.Errorf(logMsgStreamOtherError, result.err)
				signalReady(result.err)
				return
			}
			stream = result.stream

		case <-s.halt:
			if s.cacheCancel != nil {
				s.cacheCancel()
				s.cacheCancel = nil
				s.cacheCh = nil
			}
			// The SSE goroutine may still be running. Drain its result in the
			// background: if it produced a stream, close it so nothing leaks.
			go func() {
				result := <-streamCh
				if result.stream != nil {
					result.stream.Close()
				}
			}()
			return
		}
	}

	signalReady(nil)
	s.consumeStream(stream)
}

func (s *StreamManager) consumeStream(stream *es.Stream) {
	// Drain any remaining Events and Errors so the eventsource Stream's goroutines can be garbage
	// collected. This MUST run in a separate goroutine and must not block consumeStream from
	// returning: the eventsource library (v1.11.0) can deadlock internally when Close races with an
	// in-flight event. Its Stream.stream loop, on close, drains its internal errs channel before its
	// events channel (eventsource stream.go discardCurrentStream), but the decoder goroutine may be
	// blocked sending on events, so neither side progresses and stream.Events is never closed. If we
	// drained inline, "for range stream.Events" would block forever, so subscribe would never return,
	// s.done would never close, and StreamManager.Close (hence Relay.Close) would hang until the test
	// or process timed out. Backgrounding the drain leaks those wedged goroutines in that rare race,
	// but only at shutdown, and keeps Close bounded.
	defer func() {
		go func() {
			for range stream.Events {
			}
			if stream.Errors != nil {
				for range stream.Errors {
				}
			}
		}()
	}()

	for {
		select {
		case content, ok := <-s.cacheCh:
			if ok && content != nil {
				s.applyCachedContent(content)
			}
			if s.cacheCancel != nil {
				s.cacheCancel()
				s.cacheCancel = nil
			}
			s.cacheCh = nil

		case event, ok := <-stream.Events:
			if !ok {
				// COVERAGE: stream.Events is only closed if the EventSource has been closed. However, that
				// only happens when we have received from s.halt, in which case we return immediately
				// after calling stream.Close(), terminating the for loop-- so we should not actually reach
				// this point. Still, in case the channel is somehow closed unexpectedly, we do want to
				// terminate the loop.
				return
			}

			if s.handleStreamEvent(event) {
				stream.Restart()
			}
		case <-s.halt:
			if s.cacheCancel != nil {
				s.cacheCancel()
				s.cacheCancel = nil
				s.cacheCh = nil
			}
			stream.Close()
			return
		}
	}
}

// handleStreamEvent processes a single SSE event. Returns true if the stream should be restarted.
func (s *StreamManager) handleStreamEvent(event es.Event) bool {
	if s.loggers.IsDebugEnabled() {
		s.loggers.Debugf("Received %q event: %s", event.Event(), obfuscateEventData(event.Data()))
	}

	shouldRestart := false
	gotMalformedEvent := func(event es.Event, err error) {
		s.loggers.Errorf(logMsgMalformedData, event.Event(), err)
		shouldRestart = true
	}

	switch event.Event() {
	case PutEvent:
		var putMessage PutMessageData
		if err := json.Unmarshal([]byte(event.Data()), &putMessage); err != nil {
			gotMalformedEvent(event, err)
			break
		}
		if putMessage.Path != "/" {
			s.loggers.Infof(logMsgWrongPath, PutEvent, putMessage.Path)
			break
		}
		// The stream has authoritative data — cancel any in-flight cache read.
		if s.cacheCancel != nil {
			s.cacheCancel()
			s.cacheCancel = nil
			s.cacheCh = nil
		}
		putMessage.Data.Persist = true
		if s.handlePut(putMessage.Data) {
			shouldRestart = true
		}

	case PatchEvent:
		var patchMsg PatchMessageData
		var err error
		if err = json.Unmarshal([]byte(event.Data()), &patchMsg); err != nil {
			gotMalformedEvent(event, err)
			break
		}

		prefix, id := path.Split(patchMsg.Path)

		switch prefix {
		case environmentPathPrefix:
			envRep := envfactory.EnvironmentRep{}
			if err = json.Unmarshal(patchMsg.Data, &envRep); err != nil {
				gotMalformedEvent(event, err)
				break
			}
			if id != string(envRep.EnvID) {
				s.loggers.Warnf(logMsgEnvHasWrongID, envRep.EnvID, id)
				break
			}
			// Validate before Upsert so a malformed payload does not advance the version (see
			// validateCredentialPayload). Preserve previous state for this env and reconnect.
			if err = s.validateCredentialPayload(envRep); err != nil {
				s.loggers.Errorf("Received malformed credential payload for environment %q (%s); preserving previous credentials and will restart stream", envRep.EnvID, err)
				shouldRestart = true
				break
			}
			action := s.envReceiver.Upsert(id, envRep, envRep.Version)
			s.dispatchEnvAction(config.EnvironmentID(id), envRep, action)
			if action != ActionNoop {
				s.cacheUpsert(CacheKindEnvironment, id, envRep)
			}
		case filterPathPrefix:
			filterRep := envfactory.FilterRep{}
			if err = json.Unmarshal(patchMsg.Data, &filterRep); err != nil {
				gotMalformedEvent(event, err)
				break
			}
			action := s.filterReceiver.Upsert(id, filterRep, filterRep.Version)
			s.dispatchFilterAction(config.FilterID(id), filterRep, action)
			if action != ActionNoop {
				s.cacheUpsert(CacheKindFilter, id, filterRep)
			}
		default:
			// It's important for this to be a debug message, so that it is effectively silent when unrecognized
			// entities are received. If new entities are added in the future, we don't want the log blowing
			// up with warnings/errors/info.
			s.loggers.Debugf(logMsgUnknownEntity, patchMsg.Path)
		}

	case DeleteEvent:
		var deleteMessage DeleteMessageData
		if err := json.Unmarshal([]byte(event.Data()), &deleteMessage); err != nil {
			gotMalformedEvent(event, err)
			break
		}
		prefix, id := path.Split(deleteMessage.Path)
		switch prefix {
		case environmentPathPrefix:
			action := s.envReceiver.Delete(id, deleteMessage.Version)
			s.dispatchEnvAction(config.EnvironmentID(id), envfactory.EnvironmentRep{}, action)
			if action == ActionDelete {
				s.cacheDelete(CacheKindEnvironment, id)
			}
		case filterPathPrefix:
			action := s.filterReceiver.Delete(id, deleteMessage.Version)
			s.dispatchFilterAction(config.FilterID(id), envfactory.FilterRep{}, action)
			if action == ActionDelete {
				s.cacheDelete(CacheKindFilter, id)
			}
		default:
			// It's important for this to be a debug message, so that it is effectively silent when unrecognized
			// entities are received. If new entities are added in the future, we don't want the log blowing
			// up with warnings/errors/info.
			s.loggers.Debugf(logMsgUnknownEntity, deleteMessage.Path)
		}

	case ReconnectEvent:
		s.loggers.Info(logMsgDeliberateReconnect)
		shouldRestart = true

	default:
		s.loggers.Warnf(logMsgUnknownEvent, event.Event())
	}

	return shouldRestart
}

// dispatchEnvAction dispatches a single environment action to the handler.
func (s *StreamManager) dispatchEnvAction(id config.EnvironmentID, rep envfactory.EnvironmentRep, action Action) {
	switch action {
	case ActionNoop:
		return
	case ActionInsert:
		params := rep.ToParams()
		s.handler.AddEnvironment(params)
	case ActionDelete:
		s.handler.DeleteEnvironment(id)
	case ActionUpdate:
		params := rep.ToParams()
		s.handler.UpdateEnvironment(params)
	}
}

// validateCredentialPayload checks that an environment rep carries a structurally valid credential
// set. It is run at the stream parse boundary — before the rep's version is recorded via Upsert —
// mirroring how an unparseable event is handled by gotMalformedEvent.
//
// Per design §9 a malformed credential payload must preserve the previous accepted set and force a
// stream reconnect (RAC is one-way push with no NAK channel, so the reconnect is what makes the
// backend resend a fresh put). Validating here rather than after Upsert is essential: the version is
// not advanced, so the fresh put — which carries the same version — is not deduplicated away by the
// MessageReceiver. Any error from BuildAcceptedSet is a *MalformedCredentialSetError.
func (s *StreamManager) validateCredentialPayload(rep envfactory.EnvironmentRep) error {
	_, _, err := envfactory.BuildAcceptedSet(rep.ToParams())
	return err
}

func (s *StreamManager) dispatchFilterAction(id config.FilterID, rep envfactory.FilterRep, action Action) {
	switch action {
	case ActionNoop:
		return
	case ActionInsert:
		s.handler.AddFilter(rep.ToParams(id))
	case ActionDelete:
		s.handler.DeleteFilter(id)
	}
}

func (s *StreamManager) applyCachedContent(content *PutContent) {
	s.handlePut(PutContent{
		Environments: content.Environments,
		Filters:      content.Filters,
		Persist:      false,
	})
	s.loggers.Info("AutoConfig loaded from persistent cache; Relay can serve while connecting to LaunchDarkly")
}

// All of the private methods below can be assumed to be called from the same goroutine that consumeStream
// is on. We will never be processing more than one stream message at the same time.
//
// handlePut returns true if the stream should be restarted — a malformed credential payload in any of
// the environments triggers a reconnect (design §9), while still processing the well-formed ones.
func (s *StreamManager) handlePut(content PutContent) bool {
	// A "put" message represents a full environment set. We will compare them one at a time to the
	// current set of environments (if any), calling the handler's AddEnvironment for any new ones,
	// UpdateEnvironment for any that have changed, and DeleteEnvironment for any that are no longer
	// in the set.
	shouldRestart := false
	malformedEnvIDs := make(map[config.EnvironmentID]bool)
	s.loggers.Infof(logMsgPutEvent, len(content.Environments))
	for id, rep := range content.Environments {
		if id != rep.EnvID {
			s.loggers.Warnf(logMsgEnvHasWrongID, rep.EnvID, id)
			continue
		}
		// Validate before Upsert so a malformed payload does not advance the version (see
		// validateCredentialPayload). Skip this env, preserving its previous state, and reconnect.
		if err := s.validateCredentialPayload(rep); err != nil {
			s.loggers.Errorf("Received malformed credential payload for environment %q (%s); preserving previous credentials and will restart stream", rep.EnvID, err)
			shouldRestart = true
			malformedEnvIDs[id] = true
			continue
		}
		s.dispatchEnvAction(id, rep, s.envReceiver.Upsert(string(id), rep, rep.Version))
	}

	// Retain only the environments that were added in the PUT.
	for _, deleted := range s.envReceiver.Retain(func(id string) bool {
		_, ok := content.Environments[config.EnvironmentID(id)]
		return ok
	}) {
		s.dispatchEnvAction(config.EnvironmentID(deleted), envfactory.EnvironmentRep{}, ActionDelete)
	}

	for id, filter := range content.Filters {
		s.dispatchFilterAction(id, filter, s.filterReceiver.Upsert(string(id), filter, filter.Version))
	}

	// Retain only the filters that were added in the PUT.
	for _, deleted := range s.filterReceiver.Retain(func(id string) bool {
		_, ok := content.Filters[config.FilterID(id)]
		return ok
	}) {
		s.dispatchFilterAction(config.FilterID(deleted), envfactory.FilterRep{}, ActionDelete)
	}

	s.handler.ReceivedAllEnvironments()
	if content.Persist {
		s.persistPut(content, malformedEnvIDs)
	}
	return shouldRestart
}

// persistPut writes a put's content to the cache. A clean put is stored atomically with SetAll. When
// the put carried malformed environments, a plain SetAll would corrupt the cache: filtering the
// malformed envs out would drop them, and if every env was malformed it would wipe the cache entirely.
// Instead we keep each malformed env's previously-cached entry and write the resulting snapshot — so
// the valid envs and the filters in this put are persisted, the malformed envs keep their last-good
// value (or are simply omitted when the cache has no prior entry for them), and envs the put removed
// are still dropped. The reconnect a malformed put triggers fetches fresh data for the malformed envs.
// If the prior cache genuinely can't be read, leave it untouched rather than risk dropping entries.
func (s *StreamManager) persistPut(content PutContent, malformedEnvIDs map[config.EnvironmentID]bool) {
	if len(malformedEnvIDs) > 0 {
		// A nil result with no error means an empty cache (per the Cache contract), not a failure —
		// in that case there are simply no prior entries to restore. Only a real read error makes it
		// unsafe to rewrite the snapshot, so bail out only then.
		prev, err := s.cache.GetAll(context.Background())
		if err != nil {
			s.loggers.Warnf("Skipping AutoConfig cache write for a put with malformed credentials (cannot read prior cache): %v", err)
			return
		}
		envs := make(map[config.EnvironmentID]envfactory.EnvironmentRep, len(content.Environments))
		for id, rep := range content.Environments {
			if malformedEnvIDs[id] {
				if prev != nil {
					if prevRep, ok := prev.Environments[id]; ok {
						envs[id] = prevRep // keep the malformed env's last-good cached entry
					}
				}
			} else {
				envs[id] = rep
			}
		}
		content.Environments = envs
	}
	if err := s.cache.SetAll(context.Background(), content); err != nil {
		s.loggers.Warnf("Failed to write AutoConfig cache: %v", err)
	}
}

func (s *StreamManager) cacheUpsert(kind CacheKind, id string, data interface{}) {
	if err := s.cache.Upsert(context.Background(), kind, id, data); err != nil {
		s.loggers.Warnf("Failed to upsert AutoConfig cache item %q: %v", id, err)
	}
}

func (s *StreamManager) cacheDelete(kind CacheKind, id string) {
	if err := s.cache.Delete(context.Background(), kind, id); err != nil {
		s.loggers.Warnf("Failed to delete AutoConfig cache item %q: %v", id, err)
	}
}

func obfuscateEventData(data string) string {
	// Used for debug logging to obscure the SDK keys and mobile keys in the JSON data
	data = sdkKeyJSONRegex.ReplaceAllString(data, `"value":"...$1"`)
	data = mobKeyJSONRegex.ReplaceAllString(data, `"mobKey":"...$1"`)
	return data
}
