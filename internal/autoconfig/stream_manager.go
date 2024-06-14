package autoconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"sync"
	"time"

	es "github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
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
	expiredKeys       chan expiredKey
	expiryTimers      map[config.SDKKey]*time.Timer
	httpConfig        httpconfig.HTTPConfig
	initialRetryDelay time.Duration
	loggers           ldlog.Loggers
	halt              chan struct{}
	closeOnce         sync.Once

	envReceiver    *MessageReceiver[envfactory.EnvironmentRep]
	filterReceiver *MessageReceiver[envfactory.FilterRep]
}

type expiredKey struct {
	envID   config.EnvironmentID
	projKey string
	key     config.SDKKey
}

// NewStreamManager creates a StreamManager, but does not start the connection.
func NewStreamManager(
	key config.AutoConfigKey,
	streamURI *url.URL,
	handler MessageHandler,
	httpConfig httpconfig.HTTPConfig,
	initialRetryDelay time.Duration,
	protocolVersion int,
	loggers ldlog.Loggers,
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
		lastKnownEnvs:     make(map[config.EnvironmentID]envfactory.EnvironmentRep),
		expiredKeys:       make(chan expiredKey),
		expiryTimers:      make(map[config.SDKKey]*time.Timer),
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

	if err != nil {
		s.loggers.Errorf(logMsgStreamOtherError, err)
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

			if s.loggers.IsDebugEnabled() {
				s.loggers.Debugf("Received %q event: %s", event.Event(), obfuscateEventData(event.Data()))
			}

			gotMalformedEvent := func(event es.Event, err error) {
				s.loggers.Errorf(
					logMsgMalformedData,
					event.Event(),
					err,
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
					s.loggers.Infof(logMsgWrongPath, PutEvent, putMessage.Path)
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
						s.loggers.Warnf(logMsgEnvHasWrongID, envRep.EnvID, id)
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
					s.dispatchEnvAction(config.EnvironmentID(id), envfactory.EnvironmentRep{}, s.envReceiver.Delete(id, deleteMessage.Version))
				case filterPathPrefix:
					s.dispatchFilterAction(config.FilterID(id), envfactory.FilterRep{}, s.filterReceiver.Delete(id, deleteMessage.Version))
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

			if shouldRestart {
				stream.Restart()
			}
		case <-s.halt:
			stream.Close()
			for _, t := range s.expiryTimers {
				t.Stop()
			}
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
		//if s.IgnoreExpiringSDKKey(rep) {
		//	params.ExpiringSDKKey = ""
		//}
		s.handler.AddEnvironment(params)
	case ActionDelete:
		s.handler.DeleteEnvironment(id)
	case ActionUpdate:
		params := rep.ToParams()
		//if s.IgnoreExpiringSDKKey(rep) {
		//	params.ExpiringSDKKey = ""
		//}
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
	s.loggers.Infof(logMsgPutEvent, len(content.Environments))
	for id, rep := range content.Environments {
		if id != rep.EnvID {
			s.loggers.Warnf(logMsgEnvHasWrongID, rep.EnvID, id)
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

// IgnoreExpiringSDKKey implements the EnvironmentMsgAdapter's KeyChecker interface. Its main purpose is to
// create a goroutine that triggers SDK key expiration, if the EnvironmentRep specifies that. Additionally, it returns
// true if an ExpiringSDKKey should be ignored (since the expiry is stale).
func (s *StreamManager) IgnoreExpiringSDKKey(env envfactory.EnvironmentRep) bool {
	expiringKey := env.SDKKey.Expiring.Value
	expiryTime := env.SDKKey.Expiring.Timestamp

	if expiringKey == "" || expiryTime == 0 {
		return false
	}

	if _, alreadyHaveTimer := s.expiryTimers[expiringKey]; alreadyHaveTimer {
		return false
	}

	timeFromNow := time.Duration(expiryTime-ldtime.UnixMillisNow()) * time.Millisecond
	if timeFromNow <= 0 {
		// LD might sometimes tell us about an "expiring" key that has really already expired. If so,
		// just ignore it.
		return true
	}

	dateTime := time.Unix(int64(expiryTime)/1000, 0)
	s.loggers.Warnf(logMsgKeyWillExpire, last4Chars(string(expiringKey)), env.Describe(), dateTime)

	timer := time.NewTimer(timeFromNow)
	s.expiryTimers[expiringKey] = timer

	go func() {
		if _, ok := <-timer.C; ok {
			s.expiredKeys <- expiredKey{env.EnvID, env.ProjKey, expiringKey}
		}
	}()

	return false
}

func makeEnvName(rep envfactory.EnvironmentRep) string {
	return fmt.Sprintf("%s %s", rep.ProjName, rep.EnvName)
}

func last4Chars(s string) string {
	if len(s) < 4 { // COVERAGE: doesn't happen in unit tests, also can't happen with real environments
		return s
	}
	return s[len(s)-4:]
}

func obfuscateEventData(data string) string {
	// Used for debug logging to obscure the SDK keys and mobile keys in the JSON data
	data = sdkKeyJSONRegex.ReplaceAllString(data, `"value":"...$1"`)
	data = mobKeyJSONRegex.ReplaceAllString(data, `"mobKey":"...$1"`)
	return data
}
