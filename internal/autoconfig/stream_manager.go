package autoconfig

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"sync"
	"time"

	es "github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v9/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/logging"
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
	lastKnownEnvs     map[config.EnvironmentID]envfactory.EnvironmentRep
	httpConfig        httpconfig.HTTPConfig
	initialRetryDelay time.Duration
	logger            *slog.Logger
	halt              chan struct{}
	closeOnce         sync.Once

	envReceiver    *MessageReceiver[envfactory.EnvironmentRep]
	filterReceiver *MessageReceiver[envfactory.FilterRep]
}

// NewStreamManager creates a StreamManager, but does not start the connection.
func NewStreamManager(
	key config.AutoConfigKey,
	streamURI *url.URL,
	handler MessageHandler,
	httpConfig httpconfig.HTTPConfig,
	initialRetryDelay time.Duration,
	protocolVersion int,
	logger *slog.Logger,
) *StreamManager {
	logger = logger.With("component", "AutoConfiguration")
	if protocolVersion > 1 {
		streamURI.RawQuery = url.Values{
			protocolVersionParam: []string{strconv.Itoa(protocolVersion)},
		}.Encode()
	}
	s := &StreamManager{
		key:               key,
		uri:               streamURI,
		handler:           handler,
		lastKnownEnvs:     make(map[config.EnvironmentID]envfactory.EnvironmentRep),
		httpConfig:        httpConfig,
		initialRetryDelay: initialRetryDelay,
		logger:            logger,
		halt:              make(chan struct{}),
	}

	// Enforces ordering constraints on the SSE messages that are sent from the server, allowing the MessageHandler
	// to act only on state changes. This process is important for mitigating unnecessary or
	// incorrect disruptions to connected SDKs. For example, modifying an environment config that *could* be done without
	// recreating an environment *should* be done without recreating that environment.
	s.envReceiver = NewMessageReceiver[envfactory.EnvironmentRep](logger)
	s.filterReceiver = NewMessageReceiver[envfactory.FilterRep](logger)

	// The data flow is:
	//
	// SSE message ->
	//   s.envReceiver (enforce ordering constraints) ->
	//       handler (actually create/update/delete environments)

	return s
}

// Start causes the StreamManager to start trying to connect to the auto-config stream. The returned channel
// receives nil for a successful connection, or an error if it has permanently failed.
func (s *StreamManager) Start() <-chan error {
	readyCh := make(chan error, 1)
	go s.subscribe(readyCh)
	return readyCh
}

// Close permanently shuts down the stream.
func (s *StreamManager) Close() {
	s.closeOnce.Do(func() {
		close(s.halt)
	})
}

func (s *StreamManager) subscribe(readyCh chan<- error) {
	var readyOnce sync.Once
	signalReady := func(err error) { readyOnce.Do(func() { readyCh <- err }) }

	errorHandler := func(err error) es.StreamErrorHandlerResult {
		if se, ok := err.(es.SubscriptionError); ok {
			if se.Code == 401 || se.Code == 403 {
				s.logger.Error("invalid auto-configuration key; cannot get environments")
				signalReady(errors.New("invalid auto-configuration key"))
				return es.StreamErrorHandlerResult{CloseNow: true}
			}
			s.logger.Warn("HTTP error on auto-configuration stream", "statusCode", se.Code)
			return es.StreamErrorHandlerResult{CloseNow: false}
		}

		s.logger.Warn("unexpected error on auto-configuration stream", "error", err)
		return es.StreamErrorHandlerResult{CloseNow: false}
	}

	retry := s.initialRetryDelay
	if retry <= 0 {
		retry = defaultStreamRetryDelay // COVERAGE: never happens in unit tests
	}

	rpacEndpoint, err := url.JoinPath(s.uri.String(), autoConfigStreamPath)
	if err != nil {
		s.logger.Error("couldn't construct auto-configuration URL", "error", err)
		signalReady(err)
		return
	}

	req, _ := http.NewRequest("GET", rpacEndpoint, nil)
	req.Header.Set("Authorization", string(s.key))
	s.logger.Info("connecting to auto-configuration stream", "url", rpacEndpoint)

	// Client.Timeout must be zeroed out for stream connections, since it's not just a connect timeout
	// but a timeout for the entire response
	client := s.httpConfig.Client()
	client.Timeout = 0

	stream, err := es.SubscribeWithRequestAndOptions(req,
		es.StreamOptionHTTPClient(client),
		es.StreamOptionReadTimeout(streamReadTimeout),
		es.StreamOptionInitialRetry(retry),
		es.StreamOptionUseBackoff(streamMaxRetryDelay),
		es.StreamOptionUseJitter(streamJitterRatio),
		es.StreamOptionRetryResetInterval(streamRetryResetInterval),
		es.StreamOptionErrorHandler(errorHandler),
		es.StreamOptionCanRetryFirstConnection(-1),
		es.StreamOptionLogger(logging.NewEventSourceLogger(s.logger)),
	)
	if err != nil {
		s.logger.Error("unexpected error on auto-configuration stream", "error", err)
		signalReady(err)
		return
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
		case event, ok := <-stream.Events:
			if !ok {
				// COVERAGE: stream.Events is only closed if the EventSource has been closed. However, that
				// only happens when we have received from s.halt, in which case we return immediately
				// after calling stream.Close(), terminating the for loop-- so we should not actually reach
				// this point. Still, in case the channel is somehow closed unexpectedly, we do want to
				// terminate the loop.
				return
			}

			shouldRestart := false

			if s.logger.Enabled(nil, slog.LevelDebug) {
				s.logger.Debug("received SSE event", "event", event.Event(), "data", obfuscateEventData(event.Data()))
			}

			gotMalformedEvent := func(event es.Event, err error) {
				s.logger.Error("received streaming event with malformed JSON data; will restart stream",
					"event", event.Event(),
					"error", err,
				)
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
					s.logger.Info("ignoring event for unknown path", "event", PutEvent, "path", putMessage.Path)
					break
				}
				s.handlePut(putMessage.Data)

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
					s.dispatchEnvAction(config.EnvironmentID(id), envRep, s.envReceiver.Upsert(id, envRep, envRep.Version))
				case filterPathPrefix:
					filterRep := envfactory.FilterRep{}
					if err = json.Unmarshal(patchMsg.Data, &filterRep); err != nil {
						gotMalformedEvent(event, err)
						break
					}
					s.dispatchFilterAction(config.FilterID(id), filterRep, s.filterReceiver.Upsert(id, filterRep, filterRep.Version))
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
					s.dispatchEnvAction(config.EnvironmentID(id), envfactory.EnvironmentRep{}, s.envReceiver.Delete(id, deleteMessage.Version))
				case filterPathPrefix:
					s.dispatchFilterAction(config.FilterID(id), envfactory.FilterRep{}, s.filterReceiver.Delete(id, deleteMessage.Version))
				default:
					// It's important for this to be a debug message, so that it is effectively silent when unrecognized
					// entities are received. If new entities are added in the future, we don't want the log blowing
					// up with warnings/errors/info.
					s.logger.Debug("ignoring unknown entity", "path", deleteMessage.Path)
				}
			case ReconnectEvent:
				s.logger.Info("will restart auto-configuration stream to get new data due to a policy change")
				shouldRestart = true

			default:
				s.logger.Warn("ignoring unrecognized stream event", "event", event.Event())
			}

			if shouldRestart {
				stream.Restart()
			}
		case <-s.halt:
			stream.Close()
			return
		}
	}
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

// All of the private methods below can be assumed to be called from the same goroutine that consumeStream
// is on. We will never be processing more than one stream message at the same time.
func (s *StreamManager) handlePut(content PutContent) {
	// A "put" message represents a full environment set. We will compare them one at a time to the
	// current set of environments (if any), calling the handler's AddEnvironment for any new ones,
	// UpdateEnvironment for any that have changed, and DeleteEnvironment for any that are no longer
	// in the set.
	s.logger.Info("received configuration", "environmentCount", len(content.Environments))
	for id, rep := range content.Environments {
		if id != rep.EnvID {
			s.logger.Warn("ignoring environment data whose envId did not match key", "envId", rep.EnvID, "key", id)
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
}

func obfuscateEventData(data string) string {
	// Used for debug logging to obscure the SDK keys and mobile keys in the JSON data
	data = sdkKeyJSONRegex.ReplaceAllString(data, `"value":"...$1"`)
	data = mobKeyJSONRegex.ReplaceAllString(data, `"mobKey":"...$1"`)
	return data
}
