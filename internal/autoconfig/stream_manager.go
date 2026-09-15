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
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/retry"
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
	// extendedRetryDelay is the base delay used once the service has rejected the key.
	extendedRetryDelay time.Duration
	// initTimeout bounds how long Relay waits for a configuration once the service has
	// rejected the key. It is the initTimeout configuration option.
	initTimeout time.Duration
	// ignoreConnectionErrors keeps Relay running with no configuration rather than reporting
	// a failure. It is the ignoreConnectionErrors configuration option.
	ignoreConnectionErrors bool
	loggers                ldlog.Loggers
	halt                   chan struct{}
	done                   chan struct{} // closed when the subscribe goroutine exits
	closeOnce              sync.Once

	// streamCtx bounds the lifetime of the SSE connection, including any backoff wait between
	// attempts. Cancelling it is the only way to interrupt that wait: eventsource's retry loop
	// selects on the request's context and the delay timer, and nothing else.
	streamCtx    context.Context
	streamCancel context.CancelFunc

	// cacheCh receives the result of the async cache read started by Start().
	// It is consumed by consumeStream and nilled out after use.
	cacheCh     <-chan cacheReadResult
	cacheCancel context.CancelFunc

	envReceiver    *MessageReceiver[envfactory.EnvironmentRep]
	filterReceiver *MessageReceiver[envfactory.FilterRep]

	// statusLock guards status and failures. The eventsource error handler, the stream-consuming
	// goroutine, and the HTTP handler that serves the status resource all touch them.
	statusLock sync.Mutex
	status     StreamStatus
	// failures counts the failures recorded so far, so a caller that started work at one point
	// can tell whether the connection has failed since.
	failures uint64
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
	initTimeout time.Duration,
	ignoreConnectionErrors bool,
) *StreamManager {
	loggers.SetPrefix("AutoConfiguration")
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
		lastKnownEnvs:     make(map[config.EnvironmentID]envfactory.EnvironmentRep),
		httpConfig:        httpConfig,
		initialRetryDelay: initialRetryDelay,
		// The extended delay has no configuration key. Its value bounds the load a fleet of
		// Relay Proxy instances puts on a service that is rejecting its key.
		extendedRetryDelay:     streamExtendedRetryDelay,
		initTimeout:            initTimeout,
		ignoreConnectionErrors: ignoreConnectionErrors,
		loggers:                loggers,
		halt:                   make(chan struct{}),
		status: StreamStatus{
			State:      interfaces.DataSourceStateInitializing,
			StateSince: time.Now(),
		},
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

// cacheReadTimeout is how long the cache read may take before Relay treats the cache as
// unavailable. It is the initTimeout configuration option, with non-positive values replaced by
// the default: zero means "do not block" for the SDK client elsewhere in Relay, and applying
// that reading here would discard a usable cached configuration before the store could answer.
func (s *StreamManager) cacheReadTimeout() time.Duration {
	if s.initTimeout <= 0 {
		return config.DefaultInitTimeout
	}
	return s.initTimeout
}

// Start causes the StreamManager to start trying to connect to the auto-config stream. The returned channel
// receives nil for a successful connection, or an error if it has permanently failed.
//
// The cache read and stream connection are started concurrently. If the cache returns first,
// its data is applied so Relay can serve immediately. If the stream's first PUT arrives first,
// the cache read is cancelled and its result discarded.
func (s *StreamManager) Start() <-chan error {
	// Start the cache read concurrently with the stream connection.
	//
	// The read is bounded by initTimeout. Neither cache store sets a deadline of its own, so
	// without this a wedged store would leave Relay unable to decide whether it has anything
	// to serve. Bounding it here rather than in the wait loop means the loop has one less
	// thing to race, and the timeout produces the same closed channel as an empty cache.
	cacheCtx, cacheCancel := context.WithTimeout(context.Background(), s.cacheReadTimeout())
	cacheCh := make(chan cacheReadResult, 1)
	go func() {
		defer close(cacheCh)
		content, err := s.cache.GetAll(cacheCtx)
		if err != nil {
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				s.loggers.Warnf(logMsgCacheReadTimeout, s.cacheReadTimeout())
			case cacheCtx.Err() == nil:
				s.loggers.Warnf(logMsgCacheReadFailed, err)
			default:
				return // cancelled because Relay no longer needs the result
			}
			cacheCh <- cacheReadResult{unavailable: true}
			return
		}
		if content != nil {
			cacheCh <- cacheReadResult{content: content}
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
		// backoff wait that is already pending. Cancelling the stream context does.
		s.streamCancel()
		s.updateStatus(interfaces.DataSourceStateOff, interfaces.DataSourceErrorInfo{})
	})
	if s.done != nil {
		<-s.done
	}
	_ = s.cache.Close()
}

// cacheReadResult carries the outcome of the startup cache read. A closed channel with no
// value means the cache is reachable and empty. A value with unavailable set means the read
// failed or timed out, which is not the same thing: the store may well hold a usable
// configuration, so Relay must not conclude from it that there is nothing to serve.
type cacheReadResult struct {
	content     *PutContent
	unavailable bool
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

	// keyRejectedCh tells the wait loop below that the service rejected the credential. The
	// send never blocks, and one notification is enough.
	keyRejectedCh := make(chan struct{}, 1)
	errorHandler := s.newStreamErrorHandler(keyRejectedCh, extendedProfile)

	rpacEndpoint, err := url.JoinPath(s.uri.String(), autoConfigStreamPath)
	if err != nil {
		s.loggers.Errorf(logMsgBadURL, err)
		signalReady(err)
		return
	}

	req, _ := http.NewRequestWithContext(s.streamCtx, "GET", rpacEndpoint, nil)
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
			es.StreamOptionDefaultRetryProfile(normalProfile),
			es.StreamOptionRegisterRetryProfile(extendedProfile),
			es.StreamOptionRetryResetInterval(streamRetryResetInterval),
			es.StreamOptionErrorHandler(errorHandler),
			es.StreamOptionCanRetryFirstConnection(-1),
			es.StreamOptionLogger(streamLogger{dest: s.loggers.ForLevel(ldlog.Info)}),
		)
		streamCh <- streamResult{stream, err}
	}()

	// Race the cache read against the stream connection. If the cache returns before
	// the stream's first PUT, its data is applied so Relay can serve immediately.
	// The cache is only cancelled when a PUT arrives with authoritative data.
	// Relay cannot serve a request until it knows its environments. Only the cache can supply
	// them before the stream connects, so these three facts decide whether waiting is still
	// worthwhile.
	haveConfiguration := false // the cache supplied a configuration
	cacheEmpty := false        // the cache is reachable and holds nothing
	keyRejected := false       // the service rejected the credential

	// shouldGiveUp reports whether Relay has established that it cannot serve. That needs a
	// rejected credential and a cache that is reachable and empty. A cache Relay could not
	// read is deliberately not enough: the store may hold a usable configuration, and exiting
	// on a failover or a DNS blip would throw it away. Any other failure leaves Relay waiting
	// and retrying, which is what it did before this change.
	shouldGiveUp := func() bool {
		return keyRejected && cacheEmpty && !haveConfiguration && !s.ignoreConnectionErrors
	}

	giveUp := func() {
		s.loggers.Error(logMsgNoConfigGaveUp)
		signalReady(errors.New("invalid auto-configuration key"))
		// Stop the connection attempt as well. Without this the abandoned goroutine keeps
		// presenting a credential the service has already rejected, for as long as the
		// extended delays run.
		s.streamCancel()
		s.abandonStreamGoroutine(streamCh)
	}

	var stream *es.Stream
	for stream == nil {
		select {
		case result, ok := <-s.cacheCh:
			switch {
			case result.content != nil && len(result.content.Environments) > 0:
				s.applyCachedContent(result.content)
				haveConfiguration = true
			case !ok || !result.unavailable:
				// Reachable but with nothing Relay can serve. An entry holding only filters
				// counts as nothing: without environments there is no credential to accept and
				// no flag data to answer with.
				cacheEmpty = true
			}
			if s.cacheCancel != nil {
				s.cacheCancel()
				s.cacheCancel = nil
			}
			s.cacheCh = nil
			if shouldGiveUp() {
				giveUp()
				return
			}

		case <-keyRejectedCh:
			keyRejected = true
			if shouldGiveUp() {
				giveUp()
				return
			}

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
			s.abandonStreamGoroutine(streamCh)
			return
		}
	}

	signalReady(nil)
	s.consumeStream(stream)
}

func (s *StreamManager) consumeStream(stream *es.Stream) {
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
		case result, ok := <-s.cacheCh:
			// The stream is already connected here, so a late cache result is only useful if
			// it carries data; an unreadable cache changes nothing.
			if ok && result.content != nil {
				s.applyCachedContent(result.content)
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

			// Read the failure count before dispatching. Handling an event creates
			// environments, starts SDK clients and writes the cache, and the connection can die
			// while that runs; markValid then declines to report a connection that is already
			// gone as working.
			generation := s.failureGeneration()
			if s.handleStreamEvent(event) {
				stream.Restart()
				break
			}
			s.markValid(generation)
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
		// The stream has authoritative data, so cancel any in-flight cache read.
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
// set. It runs at the stream parse boundary, before Upsert records the rep's version. A malformed
// payload must not advance the version: the backend's fresh put carries the same version, and the
// MessageReceiver would deduplicate it away.
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

// newStreamErrorHandler builds the SSE error handler for one subscribe cycle.
//
// No response and no transport failure stops the stream. A rejected key can become valid
// again without Relay knowing, so a failure that is unlikely to correct itself soon moves the
// stream to the longer delays and it keeps trying. The handler reports such a failure on
// authFailureCh, because only the caller can decide whether Relay is able to serve meanwhile.
func (s *StreamManager) newStreamErrorHandler(
	keyRejectedCh chan<- struct{},
	extendedProfile *es.RetryProfile,
) func(error) es.StreamErrorHandlerResult {
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

		class, keyRejected := s.classifyAndLogStreamError(err)

		result := es.StreamErrorHandlerResult{CloseNow: false}
		if class == retry.Unexpected {
			if !loggedExtended {
				s.loggers.Info(logMsgExtendedBackoff)
				loggedExtended = true
			}
			result.ActivateProfile = extendedProfile
		}

		// Only a rejected credential can make Relay stop. Every other failure, however slowly
		// it retries, leaves Relay running: a 404 from a misconfigured stream URI or a
		// certificate problem is not a reason to take a whole fleet down.
		if keyRejected {
			select {
			case keyRejectedCh <- struct{}{}:
			default:
			}
		}
		return result
	}
}

// classifyAndLogStreamError sorts a stream failure into a retry class, logs it, and reports
// whether the service rejected the credential.
//
// The two results answer different questions. The class decides how long to wait before the
// next attempt. Only a rejected credential decides whether Relay can ever succeed, so only it
// can lead to Relay stopping.
//
// A failure that is unlikely to correct itself soon is worth an error, because it nearly
// always means a real configuration problem, even though the stream recovers on its own once
// that problem is fixed.
func (s *StreamManager) classifyAndLogStreamError(err error) (class retry.FailureClass, keyRejected bool) {
	var se es.SubscriptionError
	if !errors.As(err, &se) {
		// No transport-level failure is unexpected, so these keep the short delays.
		s.loggers.Warnf(logMsgStreamOtherError, err)
		return retry.Normal, false
	}

	class = retry.ClassifyHTTPStatus(se.Code)
	keyRejected = se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden
	switch {
	case keyRejected:
		s.loggers.Error(logMsgBadKeyWillRetry)
	case class == retry.Unexpected:
		s.loggers.Errorf(logMsgStreamHTTPError, se.Code)
	default:
		s.loggers.Warnf(logMsgStreamHTTPError, se.Code)
	}
	return class, keyRejected
}

// abandonStreamGoroutine leaves the SSE connection attempt behind. That goroutine may still
// be retrying, so its result is drained in the background and any stream it produced is
// closed, which keeps the connection and its goroutines from leaking.
func (s *StreamManager) abandonStreamGoroutine(streamCh <-chan streamResult) {
	go func() {
		result := <-streamCh
		if result.stream != nil {
			result.stream.Close()
		}
	}()
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
// handlePut returns true if the stream should be restarted. A malformed credential payload in any
// environment triggers a reconnect, and the well-formed environments are still processed.
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
		// See handleStreamEvent: validate before Upsert. Skip this env and reconnect.
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

// persistPut writes a put's content to the cache with SetAll. When the put carried malformed
// environments, persistPut first substitutes each malformed env's previously-cached entry, because a
// plain SetAll would drop those envs, or wipe the cache if every env was malformed. Envs the put
// removed are still dropped. If the prior cache cannot be read, persistPut leaves it untouched.
func (s *StreamManager) persistPut(content PutContent, malformedEnvIDs map[config.EnvironmentID]bool) {
	if len(malformedEnvIDs) > 0 {
		// A nil result with no error means an empty cache, not a failure, so there are no prior entries
		// to restore. Only a read error makes it unsafe to rewrite the snapshot.
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

// StreamStatus is the state of the auto-configuration stream connection. It uses the same types
// as the SDK data source status, so that the two report the same states and error kinds.
type StreamStatus struct {
	// State is the current connection state.
	State interfaces.DataSourceState
	// StateSince is the time when State last changed.
	StateSince time.Time
	// LastError describes the most recent failure. Its Kind is empty if there has been none. It
	// is retained after a recovery, so a caller can still see what went wrong.
	LastError interfaces.DataSourceErrorInfo
}

// Status returns the current stream status. Safe to call from any goroutine.
func (s *StreamManager) Status() StreamStatus {
	s.statusLock.Lock()
	defer s.statusLock.Unlock()
	return s.status
}

func (s *StreamManager) updateStatus(state interfaces.DataSourceState, errorInfo interfaces.DataSourceErrorInfo) {
	s.statusLock.Lock()
	defer s.statusLock.Unlock()
	s.setStatus(state, errorInfo)
}

// failureGeneration returns the number of failures recorded so far. Pass it to markValid to
// record success only if nothing has failed in the meantime.
func (s *StreamManager) failureGeneration() uint64 {
	s.statusLock.Lock()
	defer s.statusLock.Unlock()
	return s.failures
}

// markValid records that the connection works, unless a failure was recorded since the caller
// read generation.
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
// An interruption while the stream is still initializing keeps the initializing state, the same
// as the SDK data source status does: a first connection that has never succeeded must not report
// that it was once working. The error is still recorded, so the caller sees why.
func (s *StreamManager) setStatus(state interfaces.DataSourceState, errorInfo interfaces.DataSourceErrorInfo) {
	if state == "" {
		return
	}

	if s.status.State == interfaces.DataSourceStateOff {
		// OFF is terminal: Close was called, or the key was rejected and the stream will not be
		// retried. An event already in flight must not report it as working.
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
