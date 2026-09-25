package autoconfig

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"sync"
	"time"

	es "github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v9/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/logging"
	"github.com/launchdarkly/ld-relay/v9/internal/retry"
)

const (
	autoConfigStreamPath     = "/relay_auto_config"
	protocolVersionParam     = "rpacProtocolVersion"
	streamReadTimeout        = 5 * time.Minute // the LaunchDarkly stream should send a heartbeat comment every 3 minutes
	streamMaxRetryDelay      = 30 * time.Second
	streamRetryResetInterval = 60 * time.Second
	streamJitterRatio        = 0.5
	defaultStreamRetryDelay  = 1 * time.Second

	// Delays for a failure that is unlikely to correct itself soon, such as a rejected
	// auto-configuration key. The stream keeps retrying on these instead of giving up,
	// because an operator can make the key valid again without Relay knowing. The ceiling
	// bounds how much load a fleet of Relay Proxy instances puts on a service that is
	// rejecting every request.
	streamExtendedRetryDelay    = 5 * time.Minute
	streamExtendedMaxRetryDelay = 1 * time.Hour

	// malformedBackoffThreshold is the number of consecutive unusable events that moves the stream to
	// the extended delays.
	//
	// At two, relay reconnects at once the first time and slowly after that. One bad event may be a
	// single corruption, so it is worth asking again immediately. A second one means the reconnect
	// brought the same payload back, and asking faster will not change it.
	malformedBackoffThreshold = 2
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

// StreamStatus is the state of the auto-configuration stream connection. It uses the same types as
// the SDK data source status, so that the two report the same states and error kinds.
type StreamStatus struct {
	// State is the current connection state.
	State interfaces.DataSourceState
	// StateSince is the time when State last changed.
	StateSince time.Time
	// LastError describes the most recent failure. Its Kind is empty if there has been none. It is
	// retained after a recovery, so a caller can still see what went wrong.
	LastError interfaces.DataSourceErrorInfo
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
	httpConfig        httpconfig.HTTPConfig
	initialRetryDelay time.Duration
	// extendedRetryDelay is the base delay used once a failure looks unlikely to correct
	// itself soon.
	extendedRetryDelay time.Duration
	logger             *slog.Logger
	halt               chan struct{}
	done               chan struct{} // closed when the subscribe goroutine exits
	closeOnce          sync.Once

	// streamCtx bounds the lifetime of the SSE connection, including any backoff wait between
	// attempts. Cancelling it is the only way to interrupt that wait: eventsource's retry loop
	// selects on the request's context and the delay timer, and nothing else.
	streamCtx    context.Context
	streamCancel context.CancelFunc

	// statusLock guards status and failures. The eventsource error handler, the stream-consuming
	// goroutine, and the HTTP handlers that serve the status endpoint all touch them.
	statusLock sync.Mutex
	status     StreamStatus
	// failures counts the failures recorded so far. It lets a caller that started work at one point
	// in time tell whether the connection has failed since.
	failures uint64

	// consecutiveMalformedEvents counts data-driven restarts since the last event relay could use.
	// Only the consumeStream goroutine touches it, like the cache fields below.
	consecutiveMalformedEvents int

	// cacheCh receives the result of the async cache read started by Start().
	// It is consumed by consumeStream and nilled out after use.
	cacheCh     <-chan *PutContent
	cacheCancel context.CancelFunc

	envReceiver *MessageReceiver[envfactory.EnvironmentRep]
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
	logger *slog.Logger,
	cache Cache,
) *StreamManager {
	logger = logger.With("component", "AutoConfiguration")
	if protocolVersion > 1 {
		streamURI.RawQuery = url.Values{
			protocolVersionParam: []string{strconv.Itoa(protocolVersion)},
		}.Encode()
	}
	streamCtx, streamCancel := context.WithCancel(context.Background())
	s := &StreamManager{
		key:               key,
		streamCtx:         streamCtx,
		streamCancel:      streamCancel,
		uri:               streamURI,
		handler:           handler,
		cache:             cache,
		httpConfig:        httpConfig,
		initialRetryDelay: initialRetryDelay,
		// The extended delay has no configuration key. Its value bounds the load a fleet of
		// Relay Proxy instances puts on a service that is rejecting its key.
		extendedRetryDelay: streamExtendedRetryDelay,
		logger:             logger,
		halt:               make(chan struct{}),
		status: StreamStatus{
			State:      interfaces.DataSourceStateInitializing,
			StateSince: time.Now(),
		},
	}

	// Enforces ordering constraints on the SSE messages that are sent from the server, allowing the MessageHandler
	// to act only on state changes. This process is important for mitigating unnecessary or
	// incorrect disruptions to connected SDKs. For example, modifying an environment config that *could* be done without
	// recreating an environment *should* be done without recreating that environment.
	s.envReceiver = NewMessageReceiver[envfactory.EnvironmentRep](logger)

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
				s.logger.Warn("AutoConfig cache read failed (will rely on stream)", "error", err)
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
		// halt is only observed when the next attempt fails, so on its own it cannot end a
		// backoff wait that is already pending. Cancelling the stream context does. Without
		// this, Close would block on s.done for the remainder of a delay that now reaches an
		// hour.
		s.streamCancel()
		s.updateStatus(interfaces.DataSourceStateOff, interfaces.DataSourceErrorInfo{})
	})
	if s.done != nil {
		<-s.done
	}
	_ = s.cache.Close()
}

// Status returns the current state of the stream connection.
func (s *StreamManager) Status() StreamStatus {
	s.statusLock.Lock()
	defer s.statusLock.Unlock()
	return s.status
}

// updateStatus records a connection state change.
func (s *StreamManager) updateStatus(state interfaces.DataSourceState, errorInfo interfaces.DataSourceErrorInfo) {
	s.statusLock.Lock()
	defer s.statusLock.Unlock()
	s.setStatus(state, errorInfo)
}

// failureGeneration returns the number of failures recorded so far. Pass it to markValid to record
// success only if nothing has failed in the meantime.
func (s *StreamManager) failureGeneration() uint64 {
	s.statusLock.Lock()
	defer s.statusLock.Unlock()
	return s.failures
}

// markValid records that the connection works, unless a failure was recorded since the caller read
// generation.
//
// Handling one event is not instant: a dispatch creates environments, starts SDK clients, and writes
// the cache, and the connection can die while that runs. The event was delivered by a connection
// that is now gone, so it is not evidence that the stream works. Without this check the stream
// reports VALID until something else reports on it, and if the reconnect hangs, nothing ever does.
func (s *StreamManager) markValid(generation uint64) {
	s.statusLock.Lock()
	defer s.statusLock.Unlock()

	if s.failures != generation {
		return
	}
	s.setStatus(interfaces.DataSourceStateValid, interfaces.DataSourceErrorInfo{})
}

// setStatus applies a state change. The caller must hold statusLock.
//
// An interruption that happens while the stream is still initializing keeps the initializing state,
// the same as the SDK data source status does. A first connection that has never succeeded must not
// report that it was once working. The error is still recorded, so the caller sees why.
func (s *StreamManager) setStatus(state interfaces.DataSourceState, errorInfo interfaces.DataSourceErrorInfo) {
	if state == "" {
		// The SDK data source status ignores an empty state rather than reporting one. Nothing here
		// passes a zero value today; this keeps the two consistent if something ever does.
		return
	}

	if s.status.State == interfaces.DataSourceStateOff {
		// OFF is terminal: Close was called, so the stream is finished. An event that was
		// already in flight must not report the stream as working.
		return
	}

	if errorInfo.Kind != "" {
		s.failures++
	}

	if state == interfaces.DataSourceStateInterrupted &&
		s.status.State == interfaces.DataSourceStateInitializing {
		state = interfaces.DataSourceStateInitializing
	}

	if state != s.status.State {
		s.status.State = state
		s.status.StateSince = time.Now()
	}
	if errorInfo.Kind != "" {
		s.status.LastError = errorInfo
	}
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
	// signalShutdown releases a caller waiting on readyCh without reporting a failure, for the
	// case where Close is what ended the attempt. It shares readyOnce with signalReady, so
	// exactly one of the two ever touches the channel and neither can send on a closed one.
	//
	// Closing rather than sending nil matters: a non-nil error on this channel makes the caller
	// treat the failure as fatal and exit the process, and a shutdown is not that.
	signalShutdown := func() { readyOnce.Do(func() { close(readyCh) }) }

	retryDelay := s.initialRetryDelay
	if retryDelay <= 0 {
		retryDelay = defaultStreamRetryDelay // COVERAGE: never happens in unit tests
	}

	normalProfile := es.NewRetryProfile(
		es.RetryProfileBaseDelay(retryDelay),
		es.RetryProfileMaxDelay(streamMaxRetryDelay),
		es.RetryProfileJitter(streamJitterRatio),
	)
	extendedProfile := es.NewRetryProfile(
		es.RetryProfileBaseDelay(s.extendedRetryDelay),
		es.RetryProfileMaxDelay(streamExtendedMaxRetryDelay),
		es.RetryProfileJitter(streamJitterRatio),
	)
	errorHandler := s.newStreamErrorHandler(extendedProfile)

	rpacEndpoint, err := url.JoinPath(s.uri.String(), autoConfigStreamPath)
	if err != nil {
		s.logger.Error("couldn't construct auto-configuration URL", "error", err)
		s.updateStatus(interfaces.DataSourceStateOff, interfaces.DataSourceErrorInfo{
			Kind: interfaces.DataSourceErrorKindUnknown,
			Time: time.Now(),
		})
		signalReady(err)
		return
	}

	req, _ := http.NewRequestWithContext(s.streamCtx, "GET", rpacEndpoint, nil)
	req.Header.Set("Authorization", string(s.key))
	s.logger.Info("connecting to auto-configuration stream", "url", rpacEndpoint)

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
			es.StreamOptionDefaultRetryProfile(normalProfile),
			es.StreamOptionRegisterRetryProfile(extendedProfile),
			es.StreamOptionRetryResetInterval(streamRetryResetInterval),
			es.StreamOptionErrorHandler(errorHandler),
			es.StreamOptionCanRetryFirstConnection(-1),
			es.StreamOptionLogger(logging.NewEventSourceLogger(s.logger)),
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
				// Close cancels the stream context, and eventsource returns that cancellation from
				// here without consulting the error handler. It is the shutdown's own doing rather
				// than a stream failure, so it must not be reported as one: the caller exits the
				// process on a non-nil error, and this arm and the halt arm below are both ready
				// once Close has run, so which one wins is decided per-run.
				select {
				case <-s.halt:
					signalShutdown()
					return
				default:
				}
				s.logger.Error("unexpected error on auto-configuration stream", "error", result.err)
				// The error handler has already recorded why the connection failed, so this reports
				// only that the stream is permanently off, and keeps that specific error.
				s.updateStatus(interfaces.DataSourceStateOff, interfaces.DataSourceErrorInfo{})
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
			// Nothing signalled readyCh on this path before, so a caller that was still waiting
			// for the first connection waited forever.
			signalShutdown()
			return
		}
	}

	signalReady(nil)
	s.consumeStream(stream, extendedProfile)
}

func (s *StreamManager) consumeStream(stream *es.Stream, extendedProfile *es.RetryProfile) {
	// Consume remaining Events and Errors so we can garbage collect
	defer func() {
		for range stream.Events {
		} // COVERAGE: no way to cause this condition in unit tests
		if stream.Errors != nil {
			for range stream.Errors { // COVERAGE: no way to cause this condition in unit tests
			}
		}
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

			if outcome := s.handleStreamEvent(event); outcome.restart {
				if outcome.backOff {
					// The service will probably keep sending what relay just refused, so reconnecting on
					// the short curve would spin. Moving to the extended delays leaves the stream
					// retrying, which is what an operator fixing the payload needs.
					stream.ActivateProfile(extendedProfile)
				}
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

// eventOutcome says what the stream should do after an event.
//
// restart asks for a reconnect. backOff asks for that reconnect to wait.
//
// The two are separate because a restart caused by unusable data is not worth hurrying. The service
// cannot learn that relay refused the payload, so it will send the same thing again. Reconnecting on
// the short delays would spin until somebody fixes the payload.
type eventOutcome struct {
	restart bool
	backOff bool
}

// handleStreamEvent processes a single SSE event and reports what the stream should do next.
func (s *StreamManager) handleStreamEvent(event es.Event) eventOutcome {
	if s.logger.Enabled(context.TODO(), slog.LevelDebug) {
		s.logger.Debug("received SSE event", "event", event.Event(), "data", obfuscateEventData(event.Data()))
	}

	// Read this before the dispatch below, so a failure that happens while the event is being
	// handled is not overwritten by the success this event would otherwise report.
	generation := s.failureGeneration()

	outcome := eventOutcome{}
	malformed := false
	// The stream delivered an event, which is what proves the connection works. Only malformed data
	// takes that back, the same as the SDK streaming data source.
	processedEvent := true
	gotMalformedEvent := func(event es.Event, err error) {
		s.logger.Error("received streaming event with malformed JSON data; will restart stream",
			"event", event.Event(),
			"error", err,
		)
		malformed = true
		processedEvent = false
		outcome.restart = true
	}

	// gotMalformedCredentials reports a payload that parsed but whose credentials cannot produce a
	// usable set. The environment keeps the credentials it already had.
	gotMalformedCredentials := func(envID config.EnvironmentID, err error) {
		s.logger.Error("received malformed credential payload for environment; "+
			"keeping the previous credentials and restarting the stream",
			"envID", envID,
			"error", err,
		)
		malformed = true
		processedEvent = false
		outcome.restart = true
	}

	switch event.Event() {
	case PutEvent:
		var putMessage PutMessageData
		if err := json.Unmarshal([]byte(event.Data()), &putMessage); err != nil {
			gotMalformedEvent(event, err)
			break
		}
		if putMessage.Path != "/" {
			s.logger.Info("ignoring event for unknown path", "event", PutEvent, "path", putMessage.Path)
			break
		}
		// The stream has authoritative data — cancel any in-flight cache read.
		if s.cacheCancel != nil {
			s.cacheCancel()
			s.cacheCancel = nil
			s.cacheCh = nil
		}
		putMessage.Data.Persist = true
		if malformedEnvIDs := s.handlePut(putMessage.Data); len(malformedEnvIDs) > 0 {
			malformed = true
			processedEvent = false
			outcome.restart = true
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
				s.logger.Warn("ignoring environment data whose envId did not match key", "envId", envRep.EnvID, "key", id)
				break
			}
			// Validate before Upsert, which is what records the payload's version. Refer to
			// validateCredentialPayload for why advancing the version here would be unrecoverable.
			if err = validateCredentialPayload(envRep); err != nil {
				gotMalformedCredentials(envRep.EnvID, err)
				break
			}
			action := s.envReceiver.Upsert(id, envRep, envRep.Version)
			s.dispatchEnvAction(config.EnvironmentID(id), envRep, action)
			if action != ActionNoop {
				s.cacheUpsert(CacheKindEnvironment, id, envRep)
			}
		default:
			// It's important for this to be a debug message, so that it is effectively silent when unrecognized
			// entities are received. If new entities are added in the future, we don't want the log blowing
			// up with warnings/errors/info.
			s.logger.Debug("ignoring unknown entity", "path", patchMsg.Path)
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
		default:
			// It's important for this to be a debug message, so that it is effectively silent when unrecognized
			// entities are received. If new entities are added in the future, we don't want the log blowing
			// up with warnings/errors/info.
			s.logger.Debug("ignoring unknown entity", "path", deleteMessage.Path)
		}

	case ReconnectEvent:
		s.logger.Info("will restart auto-configuration stream to get new data due to a policy change")
		outcome.restart = true

	default:
		s.logger.Warn("ignoring unrecognized stream event", "event", event.Event())
	}

	// A delivered event is the only proof the connection works: eventsource reports errors, but
	// never reports that a connection came back, and it discards the stream's heartbeat comments. So
	// any event counts, including one this version does not recognize -- what it contained says
	// nothing about the connection that carried it.
	switch {
	case malformed:
		// Wait for a second unusable event before slowing down. The first one may be a single
		// corruption, which a prompt reconnect fixes. A second one means the reconnect brought the
		// same payload back.
		s.consecutiveMalformedEvents++
		outcome.backOff = s.consecutiveMalformedEvents >= malformedBackoffThreshold
		s.updateStatus(interfaces.DataSourceStateInterrupted, interfaces.DataSourceErrorInfo{
			Kind: interfaces.DataSourceErrorKindInvalidData,
			Time: time.Now(),
		})
	case processedEvent:
		s.consecutiveMalformedEvents = 0
		s.markValid(generation)
	}

	return outcome
}

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

// newStreamErrorHandler builds the SSE error handler for one subscribe cycle.
//
// Nothing stops the stream. A rejected key can become valid again without Relay knowing, and
// every other failure could clear at any time, so the stream keeps retrying in all cases. A
// failure that is unlikely to correct itself soon moves to the longer delays, which bounds the
// load a fleet puts on a service that is rejecting every request.
func (s *StreamManager) newStreamErrorHandler(extendedProfile *es.RetryProfile) func(error) es.StreamErrorHandlerResult {
	// loggedExtended keeps the notice about the longer delays to once per subscribe cycle.
	// The library returns to the normal delays itself once the connection has been healthy
	// for streamRetryResetInterval, and does not report that, so re-logging would mislead.
	loggedExtended := false

	return func(err error) es.StreamErrorHandlerResult {
		// If Close() has been called, stop retrying so the SSE goroutine can exit.
		select {
		case <-s.halt:
			return es.StreamErrorHandlerResult{CloseNow: true}
		default:
		}

		// Interrupted rather than Off even for a rejected key: the stream keeps retrying, so
		// it is not finished. Off is left for Close.
		s.updateStatus(interfaces.DataSourceStateInterrupted, streamErrorInfo(err))

		result := es.StreamErrorHandlerResult{CloseNow: false}
		if s.classifyAndLogStreamError(err) == retry.Unexpected {
			if !loggedExtended {
				s.logger.Info("classified failure as unexpected; engaging extended backoff")
				loggedExtended = true
			}
			result.ActivateProfile = extendedProfile
		}
		return result
	}
}

// classifyAndLogStreamError sorts a stream failure into a retry class and logs it. The class
// decides how long to wait before the next attempt; it never decides whether to keep trying.
//
// A failure that is unlikely to correct itself soon is worth an error, because it nearly
// always means a real configuration problem, even though the stream recovers on its own once
// that problem is fixed.
func (s *StreamManager) classifyAndLogStreamError(err error) retry.FailureClass {
	var se es.SubscriptionError
	if !errors.As(err, &se) {
		// No transport-level failure is unexpected, so these keep the short delays.
		s.logger.Warn("unexpected error on auto-configuration stream", "error", err)
		return retry.Normal
	}

	class := retry.ClassifyHTTPStatus(se.Code)
	switch {
	case se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden:
		s.logger.Error("invalid auto-configuration key; will keep retrying in case it becomes valid")
	case class == retry.Unexpected:
		s.logger.Error("HTTP error on auto-configuration stream", "statusCode", se.Code)
	default:
		s.logger.Warn("HTTP error on auto-configuration stream", "statusCode", se.Code)
	}
	return class
}

// streamErrorInfo describes a stream failure in the shape the SDK data source status uses, so
// the status resource reports both the same way. An HTTP failure carries its status code; a
// transport failure has none to carry.
func streamErrorInfo(err error) interfaces.DataSourceErrorInfo {
	var se es.SubscriptionError
	if errors.As(err, &se) {
		return interfaces.DataSourceErrorInfo{
			Kind:       interfaces.DataSourceErrorKindErrorResponse,
			StatusCode: se.Code,
			Time:       time.Now(),
		}
	}
	return interfaces.DataSourceErrorInfo{
		Kind: interfaces.DataSourceErrorKindNetworkError,
		Time: time.Now(),
	}
}

func (s *StreamManager) applyCachedContent(content *PutContent) {
	s.handlePut(PutContent{
		Environments: content.Environments,
		Persist:      false,
	})
	s.logger.Info("AutoConfig loaded from persistent cache; Relay can serve while connecting to LaunchDarkly")
}

// All of the private methods below can be assumed to be called from the same goroutine that consumeStream
// is on. We will never be processing more than one stream message at the same time.
// handlePut applies a full environment set, and returns the environments whose credential payload it
// refused. A refused environment keeps the credentials it already had; every other environment in the
// put is applied as usual.
func (s *StreamManager) handlePut(content PutContent) map[config.EnvironmentID]bool {
	// A "put" message represents a full environment set. We will compare them one at a time to the
	// current set of environments (if any), calling the handler's AddEnvironment for any new ones,
	// UpdateEnvironment for any that have changed, and DeleteEnvironment for any that are no longer
	// in the set.
	var malformedEnvIDs map[config.EnvironmentID]bool
	s.logger.Info("received configuration", "environmentCount", len(content.Environments))
	for id, rep := range content.Environments {
		if id != rep.EnvID {
			s.logger.Warn("ignoring environment data whose envId did not match key", "envId", rep.EnvID, "key", id)
			continue
		}
		// Validate before Upsert, which is what records the payload's version. Refer to
		// validateCredentialPayload.
		if err := validateCredentialPayload(rep); err != nil {
			s.logger.Error("received malformed credential payload for environment in configuration; "+
				"keeping the previous credentials",
				"envID", id,
				"error", err,
			)
			if malformedEnvIDs == nil {
				malformedEnvIDs = make(map[config.EnvironmentID]bool)
			}
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

	s.handler.ReceivedAllEnvironments()
	if content.Persist {
		s.persistPut(content, malformedEnvIDs)
	}
	return malformedEnvIDs
}

// validateCredentialPayload reports whether an environment's credentials can produce a usable
// accepted set. It runs at the stream parse boundary, before MessageReceiver.Upsert records the
// payload's version.
//
// The ordering is the whole point. Upsert deduplicates by version, so a rejected payload that had
// already advanced the version would make LaunchDarkly's replay of that same payload a no-op: the
// environment would keep serving credentials it should have replaced, with no path back short of
// restarting the process. Validating first leaves the version where it was, so the replay that
// follows the reconnect is applied.
func validateCredentialPayload(rep envfactory.EnvironmentRep) error {
	_, _, err := envfactory.BuildAcceptedSet(rep.ToParams())
	return err
}

// persistPut writes a put to the cache, substituting the last-good cached entry for each environment
// whose credentials were refused.
//
// A plain SetAll would drop those environments from the cache, or empty it when every environment in
// the put was refused, which would leave a restarting relay with nothing to serve from until it
// reached LaunchDarkly. Environments the put genuinely removed are still dropped.
//
// A read failure on the prior cache means there is no safe snapshot to assemble, so the cache is left
// as it is. A nil result with no error is an empty cache rather than a failure, and there is simply
// nothing to carry forward.
func (s *StreamManager) persistPut(content PutContent, malformedEnvIDs map[config.EnvironmentID]bool) {
	if len(malformedEnvIDs) > 0 {
		previous, err := s.cache.GetAll(context.Background())
		if err != nil {
			s.logger.Warn("skipping AutoConfig cache write for a configuration with malformed credentials, "+
				"because the previous cache could not be read", "error", err)
			return
		}
		environments := make(map[config.EnvironmentID]envfactory.EnvironmentRep, len(content.Environments))
		for id, rep := range content.Environments {
			if !malformedEnvIDs[id] {
				environments[id] = rep
				continue
			}
			if previous != nil {
				if previousRep, ok := previous.Environments[id]; ok {
					environments[id] = previousRep
				}
			}
		}
		content.Environments = environments
	}
	if err := s.cache.SetAll(context.Background(), content); err != nil {
		s.logger.Warn("failed to write AutoConfig cache", "error", err)
	}
}

func (s *StreamManager) cacheUpsert(kind CacheKind, id string, data interface{}) {
	if err := s.cache.Upsert(context.Background(), kind, id, data); err != nil {
		s.logger.Warn("failed to upsert AutoConfig cache item", "id", id, "error", err)
	}
}

func (s *StreamManager) cacheDelete(kind CacheKind, id string) {
	if err := s.cache.Delete(context.Background(), kind, id); err != nil {
		s.logger.Warn("failed to delete AutoConfig cache item", "id", id, "error", err)
	}
}

func obfuscateEventData(data string) string {
	// Used for debug logging to obscure the SDK keys and mobile keys in the JSON data
	data = sdkKeyJSONRegex.ReplaceAllString(data, `"value":"...$1"`)
	data = mobKeyJSONRegex.ReplaceAllString(data, `"mobKey":"...$1"`)
	return data
}
