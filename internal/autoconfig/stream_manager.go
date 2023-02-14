package autoconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
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
	autoConfigStreamPath = "/relay_auto_config"

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
	uri               string
	handler           MessageHandler
	lastKnownEnvs     map[config.EnvironmentID]envfactory.EnvironmentRep
	expiredKeys       chan expiredKey
	expiryTimers      map[config.SDKKey]*time.Timer
	httpConfig        httpconfig.HTTPConfig
	initialRetryDelay time.Duration
	loggers           ldlog.Loggers
	halt              chan struct{}
	closeOnce         sync.Once

	receiver *MessageReceiver[envfactory.EnvironmentRep]
}

type expiredKey struct {
	envID config.EnvironmentID
	key   config.SDKKey
}

// NewStreamManager creates a StreamManager, but does not start the connection.
func NewStreamManager(
	key config.AutoConfigKey,
	streamURI string,
	handler MessageHandler,
	httpConfig httpconfig.HTTPConfig,
	initialRetryDelay time.Duration,
	loggers ldlog.Loggers,
) *StreamManager {
	loggers.SetPrefix("[AutoConfiguration]")
	s := &StreamManager{
		key:               key,
		uri:               strings.TrimSuffix(streamURI, "/") + autoConfigStreamPath,
		handler:           handler,
		lastKnownEnvs:     make(map[config.EnvironmentID]envfactory.EnvironmentRep),
		expiredKeys:       make(chan expiredKey),
		expiryTimers:      make(map[config.SDKKey]*time.Timer),
		httpConfig:        httpConfig,
		initialRetryDelay: initialRetryDelay,
		loggers:           loggers,
		halt:              make(chan struct{}),
	}

	// Converts EnvironmentRep data (the JSON model) to EnvironmentParams (internal model used by the rest of the code).
	// Additionally, invokes StreamManager's IgnoreExpiringSDKKey method whenever a message is received.
	// See EnvironmentMsgAdapter's docs for more context.
	envMsgAdapter := NewEnvironmentMsgAdapter(handler, s, loggers)

	// Enforces ordering constraints on the SSE messages that are sent from the server, allowing the MessageHandler
	// (via envMsgAdapter) to act only on state changes. This process is important for mitigating unnecessary or
	// incorrect disruptions to connected SDKs. For example, modifying an environment config that *could* be done without
	// recreating an environment *should* be done without recreating that environment.
	s.receiver = NewMessageReceiver[envfactory.EnvironmentRep](envMsgAdapter, loggers)

	// The data flow is:
	//
	// SSE message ->
	//   s.receiver (enforce ordering constraints) ->
	//     envMsgAdapter (convert to internal data representation, possibly start key expiry timers) ->
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
	req, _ := http.NewRequest("GET", s.uri, nil)
	req.Header.Set("Authorization", string(s.key))
	s.loggers.Infof(logMsgStreamConnecting, s.uri)

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
				s.handlePut(putMessage.Data.Environments)

			case PatchEvent:
				var patchMessage PatchMessageData
				if err := json.Unmarshal([]byte(event.Data()), &patchMessage); err != nil {
					gotMalformedEvent(event, err)
					break
				}
				if !strings.HasPrefix(patchMessage.Path, environmentPathPrefix) {
					s.loggers.Infof(logMsgWrongPath, PatchEvent, patchMessage.Path)
					break
				}
				envID := config.EnvironmentID(strings.TrimPrefix(patchMessage.Path, environmentPathPrefix))
				if patchMessage.Data.EnvID != envID {
					s.loggers.Warnf(logMsgEnvHasWrongID, patchMessage.Data.EnvID, envID)
					break
				}
				s.receiver.Upsert(patchMessage.Data, patchMessage.Data.Version)

			case DeleteEvent:
				var deleteMessage DeleteMessageData
				if err := json.Unmarshal([]byte(event.Data()), &deleteMessage); err != nil {
					gotMalformedEvent(event, err)
					break
				}
				if !strings.HasPrefix(deleteMessage.Path, environmentPathPrefix) {
					s.loggers.Infof(logMsgWrongPath, DeleteEvent, deleteMessage.Path)
					break
				}
				envID := strings.TrimPrefix(deleteMessage.Path, environmentPathPrefix)
				s.receiver.Delete(envID, deleteMessage.Version)

			case ReconnectEvent:
				s.loggers.Info(logMsgDeliberateReconnect)
				shouldRestart = true
				stream.Restart()

			default:
				s.loggers.Warnf(logMsgUnknownEvent, event.Event())
			}

			if shouldRestart {
				stream.Restart()
			}

		case expiredKey := <-s.expiredKeys:
			s.loggers.Warnf(logMsgKeyExpired, last4Chars(string(expiredKey.key)), expiredKey.envID,
				makeEnvName(s.lastKnownEnvs[expiredKey.envID]))
			s.handler.KeyExpired(expiredKey.envID, expiredKey.key)

		case <-s.halt:
			stream.Close()
			for _, t := range s.expiryTimers {
				t.Stop()
			}
			return
		}
	}
}

// All of the private methods below can be assumed to be called from the same goroutine that consumeStream
// is on. We will never be processing more than one stream message at the same time.

func (s *StreamManager) handlePut(allEnvReps map[config.EnvironmentID]envfactory.EnvironmentRep) {
	// A "put" message represents a full environment set. We will compare them one at a time to the
	// current set of environments (if any), calling the handler's AddEnvironment for any new ones,
	// UpdateEnvironment for any that have changed, and DeleteEnvironment for any that are no longer
	// in the set.
	s.loggers.Infof(logMsgPutEvent, len(allEnvReps))
	for id, rep := range allEnvReps {
		if id != rep.EnvID {
			s.loggers.Warnf(logMsgEnvHasWrongID, rep.EnvID, id)
			continue
		}
		s.receiver.Upsert(rep, rep.Version)
	}
	s.receiver.Purge(func(id string) bool {
		_, newlyAdded := allEnvReps[config.EnvironmentID(id)]
		return !newlyAdded
	})
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
			s.expiredKeys <- expiredKey{env.EnvID, expiringKey}
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
