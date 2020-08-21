package autoconfig

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	config "github.com/launchdarkly/ld-relay-config"
	"github.com/launchdarkly/ld-relay-core/httpconfig"
	"github.com/launchdarkly/ld-relay-core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/enterprise/entconfig"

	es "github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
)

const (
	autoConfigStreamPath = "/relay_auto_config"

	streamReadTimeout        = 5 * time.Minute // the LaunchDarkly stream should send a heartbeat comment every 3 minutes
	streamMaxRetryDelay      = 30 * time.Second
	streamRetryResetInterval = 60 * time.Second
	streamJitterRatio        = 0.5
	defaultStreamRetryDelay  = 1 * time.Second

	logMsgStreamConnecting    = "Connecting to auto-configuration stream at %s"
	logMsgStreamHTTPError     = "HTTP error %d on auto-configuration stream"
	logMsgStreamOtherError    = "Unexpected error on auto-configuration stream: %s"
	logMsgBadKey              = "Invalid auto-configuration key; cannot get environments"
	logMsgDeliberateReconnect = "Will restart auto-configuration stream to get new data due to a policy change"
	logMsgPutEvent            = "Received configuration for %d environment(s)"
	logMsgAddEnv              = "Added environment %s (%s)"
	logMsgUpdateEnv           = "Properties have changed for environment %s (%s)"
	logMsgUpdateBadVersion    = "Ignoring out-of-order update for environment %s (%s)"
	logMsgDeleteEnv           = "Removing environment %s (%s)"
	logMsgDeleteBadVersion    = "Ignoring out-of-order delete for environment %s (%s)"
	logMsgKeyWillExpire       = "Old SDK key ending in %s for environment %s (%s) will expire at %s"
	logMsgKeyExpired          = "Old SDK key ending in %s for environment %s (%s) has expired"
	logMsgEnvHasWrongID       = "Ignoring environment data whose envId %q did not match key %q"
	logMsgUnknownEvent        = "Ignoring unrecognized stream event: %q"
	logMsgWrongPath           = "Ignoring %q event for unknown path %q"
	logMsgMalformedData       = "Received streaming %q event with malformed JSON data (%s); will restart stream"
)

var (
	// These regexes are used for obfuscating keys in debug logging
	sdkKeyJSONRegex = regexp.MustCompile(`"value": *"[^"]*([^"][^"][^"][^"])"`)  //nolint:gochecknoglobals
	mobKeyJSONRegex = regexp.MustCompile(`"mobKey": *"[^"]*([^"][^"][^"][^"])"`) //nolint:gochecknoglobals
)

// StreamManager manages the auto-configuration SSE stream.
//
// That includes managing the stream connection itself (reconnecting as needed, the same as the SDK streams),
// and also maintaining the last known state of information received from the stream so that it can determine
// whether an update is really an update (that is, checking version numbers and diffing the contents of a
// "put" event against the previous state).
//
// Relay Enterprise provides an implementation of the MessageHandler interface which will be called for all
// changes that it needs to know about.
type StreamManager struct {
	key               entconfig.AutoConfigKey
	uri               string
	handler           MessageHandler
	lastKnownEnvs     map[config.EnvironmentID]EnvironmentRep
	expiredKeys       chan expiredKey
	expiryTimers      map[config.SDKKey]*time.Timer
	httpConfig        httpconfig.HTTPConfig
	initialRetryDelay time.Duration
	loggers           ldlog.Loggers
	halt              chan struct{}
	closeOnce         sync.Once
}

type expiredKey struct {
	envID config.EnvironmentID
	key   config.SDKKey
}

// NewStreamManager creates a StreamManager, but does not start the connection.
func NewStreamManager(
	key entconfig.AutoConfigKey,
	streamURI string,
	handler MessageHandler,
	httpConfig httpconfig.HTTPConfig,
	initialRetryDelay time.Duration,
	loggers ldlog.Loggers,
) *StreamManager {
	baseURI := streamURI
	if baseURI == "" {
		baseURI = config.DefaultStreamURI // COVERAGE: never happens in unit tests
	}
	loggers.SetPrefix("[AutoConfiguration]")
	return &StreamManager{
		key:               key,
		uri:               strings.TrimSuffix(baseURI, "/") + autoConfigStreamPath,
		handler:           handler,
		lastKnownEnvs:     make(map[config.EnvironmentID]EnvironmentRep),
		expiredKeys:       make(chan expiredKey),
		expiryTimers:      make(map[config.SDKKey]*time.Timer),
		httpConfig:        httpConfig,
		initialRetryDelay: initialRetryDelay,
		loggers:           loggers,
		halt:              make(chan struct{}),
	}
}

// Start causes the StreamManager to start trying to connect to the auto-config stream. The returned channel
// receives a value when the connection has either been made or permanently failed.
func (s *StreamManager) Start() <-chan struct{} {
	readyCh := make(chan struct{}, 1)
	go s.subscribe(readyCh)
	return readyCh
}

// Close permanently shuts down the stream.
func (s *StreamManager) Close() {
	s.closeOnce.Do(func() {
		close(s.halt)
	})
}

func (s *StreamManager) subscribe(readyCh chan<- struct{}) {
	req, _ := http.NewRequest("GET", s.uri, nil)
	req.Header.Set("Authorization", string(s.key))
	s.loggers.Infof(logMsgStreamConnecting, s.uri)

	var readyOnce sync.Once
	signalReady := func() { readyOnce.Do(func() { readyCh <- struct{}{} }) }

	errorHandler := func(err error) es.StreamErrorHandlerResult {
		if se, ok := err.(es.SubscriptionError); ok {
			if se.Code == 401 || se.Code == 403 {
				s.loggers.Error(logMsgBadKey)
				signalReady()
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
		signalReady()
		return
	}

	signalReady()
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
				s.addOrUpdate(patchMessage.Data)

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
				envID := config.EnvironmentID(strings.TrimPrefix(deleteMessage.Path, environmentPathPrefix))
				s.handleDelete(envID, deleteMessage.Version)

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

func (s *StreamManager) handlePut(allEnvReps map[config.EnvironmentID]EnvironmentRep) {
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
		if s.lastKnownEnvs[id] == rep {
			// Unchanged - don't try to update because we would get a warning for the version not being higher
			continue
		}
		s.addOrUpdate(rep)
	}
	for id, currentEnv := range s.lastKnownEnvs {
		if _, isInNewData := allEnvReps[id]; !isInNewData && !isTombstone(currentEnv) {
			s.handleDelete(id, -1)
		}
	}
}

func (s *StreamManager) addOrUpdate(rep EnvironmentRep) {
	params := makeEnvironmentParams(rep)

	// Check whether this is a new environment or an update
	currentEnv, exists := s.lastKnownEnvs[rep.EnvID]
	if exists {
		// Check version to make sure this isn't an out-of-order message
		if rep.Version <= currentEnv.Version {
			s.loggers.Infof(logMsgUpdateBadVersion, rep.EnvID, makeEnvName(currentEnv))
			return
		}
		if currentEnv.EnvID == "" {
			// This was a tombstone, so we are effectively adding a new environment.
			exists = false
		}
	}

	expiringKey := rep.SDKKey.Expiring.Value
	expiryTime := rep.SDKKey.Expiring.Timestamp
	if expiringKey != "" && expiryTime != 0 {
		if _, alreadyHaveTimer := s.expiryTimers[expiringKey]; !alreadyHaveTimer {
			timeFromNow := time.Duration(expiryTime-ldtime.UnixMillisNow()) * time.Millisecond
			dateTime := time.Unix(int64(expiryTime)/1000, 0)
			s.loggers.Warnf(logMsgKeyWillExpire, last4Chars(string(expiringKey)), rep.EnvID,
				params.Identifiers.GetDisplayName(), dateTime)
			timer := time.NewTimer(timeFromNow)
			s.expiryTimers[expiringKey] = timer
			go func() {
				if _, ok := <-timer.C; ok {
					s.expiredKeys <- expiredKey{rep.EnvID, expiringKey}
				}
			}()
		}
	}

	if exists {
		s.lastKnownEnvs[rep.EnvID] = rep
		s.loggers.Infof(logMsgUpdateEnv, rep.EnvID, params.Identifiers.GetDisplayName())
		s.handler.UpdateEnvironment(params)
	} else {
		s.lastKnownEnvs[rep.EnvID] = rep
		s.loggers.Infof(logMsgAddEnv, rep.EnvID, params.Identifiers.GetDisplayName())
		s.handler.AddEnvironment(params)
	}
}

func (s *StreamManager) handleDelete(envID config.EnvironmentID, version int) {
	currentEnv, exists := s.lastKnownEnvs[envID]
	// Check version to make sure this isn't an out-of-order message
	if version > 0 {
		if exists && version == currentEnv.Version && isTombstone(currentEnv) {
			// This is a tombstone, it's already been deleted, no need for a warning
			return
		}
		if exists && version <= currentEnv.Version {
			// The existing environment (or tombstone) has too high a version number; don't delete
			s.loggers.Infof(logMsgDeleteBadVersion, envID, makeEnvName(currentEnv))
			return
		}
		// Store a tombstone with the version, to prevent later out-of-order updates; we do this even
		// if we never heard of this environment, in case there are out-of-order messages and the
		// event to add the environment comes later
		s.lastKnownEnvs[envID] = makeTombstone(version)
	}
	if exists {
		s.loggers.Infof(logMsgDeleteEnv, envID, makeEnvName(currentEnv))
		s.handler.DeleteEnvironment(envID)
	}
}

func makeEnvironmentParams(rep EnvironmentRep) EnvironmentParams {
	return EnvironmentParams{
		EnvID: rep.EnvID,
		Identifiers: relayenv.EnvIdentifiers{
			EnvKey:   rep.EnvKey,
			EnvName:  rep.EnvName,
			ProjKey:  rep.ProjKey,
			ProjName: rep.ProjName,
		},
		SDKKey:         rep.SDKKey.Value,
		MobileKey:      rep.MobKey,
		ExpiringSDKKey: rep.SDKKey.Expiring.Value,
		TTL:            time.Duration(rep.DefaultTTL) * time.Minute,
		SecureMode:     rep.SecureMode,
	}
}

func makeEnvName(rep EnvironmentRep) string {
	return fmt.Sprintf("%s %s", rep.ProjName, rep.EnvName)
}

func makeTombstone(version int) EnvironmentRep {
	return EnvironmentRep{Version: version}
}

func isTombstone(rep EnvironmentRep) bool {
	return rep.EnvID == ""
}

func last4Chars(s string) string {
	if len(s) < 4 {
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
