package internal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	es "github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// Implementation of the streaming data source, not including the lower-level SSE implementation which is in
// the eventsource package.
//
// Error handling works as follows:
// 1. If any event is malformed, we must assume the stream is broken and we may have missed updates. Set the
// data source state to INTERRUPTED, with an error kind of INVALID_DATA, and restart the stream.
// 2. If we try to put updates into the data store and we get an error, we must assume something's wrong with the
// data store. We don't have to log this error because it is logged by DataSourceUpdatesImpl, which will also set
// our state to INTERRUPTED for us.
// 2a. If the data store supports status notifications (which all persistent stores normally do), then we can
// assume it has entered a failed state and will notify us once it is working again. If and when it recovers, then
// it will tell us whether we need to restart the stream (to ensure that we haven't missed any updates), or
// whether it has already persisted all of the stream updates we received during the outage.
// 2b. If the data store doesn't support status notifications (which is normally only true of the in-memory store)
// then we don't know the significance of the error, but we must assume that updates have been lost, so we'll
// restart the stream.
// 3. If we receive an unrecoverable error like HTTP 401, we close the stream and don't retry, and set the state
// to OFF. Any other HTTP error or network error causes a retry with backoff, with a state of INTERRUPTED.
// 4. We set the Future returned by start() to tell the client initialization logic that initialization has either
// succeeded (we got an initial payload and successfully stored it) or permanently failed (we got a 401, etc.).
// Otherwise, the client initialization method may time out but we will still be retrying in the background, and
// if we succeed then the client can detect that we're initialized now by calling our Initialized method.

const (
	putEvent                 = "put"
	patchEvent               = "patch"
	deleteEvent              = "delete"
	streamReadTimeout        = 5 * time.Minute // the LaunchDarkly stream should send a heartbeat comment every 3 minutes
	streamMaxRetryDelay      = 30 * time.Second
	streamRetryResetInterval = 60 * time.Second
	streamJitterRatio        = 0.5
	defaultStreamRetryDelay  = 1 * time.Second

	streamingErrorContext     = "in stream connection"
	streamingWillRetryMessage = "will retry"
)

// StreamProcessor is the internal implementation of the streaming data source.
//
// This type is exported from internal so that the StreamingDataSourceBuilder tests can verify its
// configuration. All other code outside of this package should interact with it only via the
// DataSource interface.
type StreamProcessor struct {
	dataSourceUpdates          interfaces.DataSourceUpdates
	streamURI                  string
	initialReconnectDelay      time.Duration
	client                     *http.Client
	headers                    http.Header
	diagnosticsManager         *ldevents.DiagnosticsManager
	loggers                    ldlog.Loggers
	setInitializedOnce         sync.Once
	isInitialized              bool
	halt                       chan struct{}
	storeStatusCh              <-chan interfaces.DataStoreStatus
	connectionAttemptStartTime ldtime.UnixMillisecondTime
	connectionAttemptLock      sync.Mutex
	readyOnce                  sync.Once
	closeOnce                  sync.Once
}

type putData struct {
	Path string  `json:"path"`
	Data allData `json:"data"`
}

type patchData struct {
	Path string `json:"path"`
	// This could be a flag or a segment, or something else, depending on the path
	Data json.RawMessage `json:"data"`
}

type deleteData struct {
	Path    string `json:"path"`
	Version int    `json:"version"`
}

// NewStreamProcessor creates the internal implementation of the streaming data source.
func NewStreamProcessor(
	context interfaces.ClientContext,
	dataSourceUpdates interfaces.DataSourceUpdates,
	streamURI string,
	initialReconnectDelay time.Duration,
) *StreamProcessor {
	sp := &StreamProcessor{
		dataSourceUpdates:     dataSourceUpdates,
		streamURI:             streamURI,
		initialReconnectDelay: initialReconnectDelay,
		headers:               context.GetHTTP().GetDefaultHeaders(),
		loggers:               context.GetLogging().GetLoggers(),
		halt:                  make(chan struct{}),
	}
	if hdm, ok := context.(hasDiagnosticsManager); ok {
		sp.diagnosticsManager = hdm.GetDiagnosticsManager()
	}

	sp.client = context.GetHTTP().CreateHTTPClient()
	// Client.Timeout isn't just a connect timeout, it will break the connection if a full response
	// isn't received within that time (which, with the stream, it never will be), so we must make
	// sure it's zero and not the usual configured default. What we do want is a *connection* timeout,
	// which is set by Config.newHTTPClient as a property of the Dialer.
	sp.client.Timeout = 0

	return sp
}

//nolint:golint,stylecheck // no doc comment for standard method
func (sp *StreamProcessor) IsInitialized() bool {
	return sp.isInitialized
}

//nolint:golint,stylecheck // no doc comment for standard method
func (sp *StreamProcessor) Start(closeWhenReady chan<- struct{}) {
	sp.loggers.Info("Starting LaunchDarkly streaming connection")
	if sp.dataSourceUpdates.GetDataStoreStatusProvider().IsStatusMonitoringEnabled() {
		sp.storeStatusCh = sp.dataSourceUpdates.GetDataStoreStatusProvider().AddStatusListener()
	}
	go sp.subscribe(closeWhenReady)
}

type parsedPath struct {
	key  string
	kind interfaces.StoreDataKind
}

func parsePath(path string) (parsedPath, error) {
	parsedPath := parsedPath{}
	switch {
	case strings.HasPrefix(path, "/segments/"):
		parsedPath.kind = interfaces.DataKindSegments()
		parsedPath.key = strings.TrimPrefix(path, "/segments/")
	case strings.HasPrefix(path, "/flags/"):
		parsedPath.kind = interfaces.DataKindFeatures()
		parsedPath.key = strings.TrimPrefix(path, "/flags/")
	default:
		return parsedPath, fmt.Errorf("unrecognized path %s", path)
	}
	return parsedPath, nil
}

func (sp *StreamProcessor) consumeStream(stream *es.Stream, closeWhenReady chan<- struct{}) {
	// Consume remaining Events and Errors so we can garbage collect
	defer func() {
		for range stream.Events {
		}
		if stream.Errors != nil {
			for range stream.Errors {
			}
		}
	}()

	for {
		select {
		case event, ok := <-stream.Events:
			if !ok {
				sp.loggers.Info("Event stream closed")
				return // The stream only gets closed without an error happening if we're being shut down externally
			}
			sp.logConnectionResult(true)

			processedEvent := true
			shouldRestart := false

			gotMalformedEvent := func(event es.Event, err error) {
				sp.loggers.Errorf(
					"Received streaming \"%s\" event with malformed JSON data (%s); will restart stream",
					event.Event(),
					err,
				)

				errorInfo := interfaces.DataSourceErrorInfo{
					Kind:    interfaces.DataSourceErrorKindInvalidData,
					Message: err.Error(),
					Time:    time.Now(),
				}
				sp.dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateInterrupted, errorInfo)

				shouldRestart = true // scenario 1 in error handling comments at top of file
				processedEvent = false
			}

			storeUpdateFailed := func(updateDesc string) {
				if sp.storeStatusCh != nil {
					sp.loggers.Errorf("Failed to store %s in data store; will try again once data store is working", updateDesc)
					// scenario 2a in error handling comments at top of file
				} else {
					sp.loggers.Errorf("Failed to store %s in data store; will restart stream until successful", updateDesc)
					shouldRestart = true // scenario 2b
					processedEvent = false
				}
			}

			switch event.Event() {
			case putEvent:
				var put putData
				if err := json.Unmarshal([]byte(event.Data()), &put); err != nil {
					gotMalformedEvent(event, err)
					break
				}
				if sp.dataSourceUpdates.Init(makeAllStoreData(put.Data.Flags, put.Data.Segments)) {
					sp.setInitializedAndNotifyClient(true, closeWhenReady)
				} else {
					storeUpdateFailed("initial streaming data")
				}

			case patchEvent:
				var patch patchData
				if err := json.Unmarshal([]byte(event.Data()), &patch); err != nil {
					gotMalformedEvent(event, err)
					break
				}
				path, err := parsePath(patch.Path)
				if err != nil {
					gotMalformedEvent(event, err)
					break
				}
				item, err := path.kind.Deserialize(patch.Data)
				if err != nil {
					gotMalformedEvent(event, err)
					break
				}
				if !sp.dataSourceUpdates.Upsert(path.kind, path.key, item) {
					storeUpdateFailed("streaming update of " + path.key)
				}

			case deleteEvent:
				var data deleteData
				if err := json.Unmarshal([]byte(event.Data()), &data); err != nil {
					gotMalformedEvent(event, err)
					break
				}
				path, err := parsePath(data.Path)
				if err != nil {
					gotMalformedEvent(event, err)
					break
				}
				deletedItem := interfaces.StoreItemDescriptor{Version: data.Version, Item: nil}
				if !sp.dataSourceUpdates.Upsert(path.kind, path.key, deletedItem) {
					storeUpdateFailed("streaming deletion of " + path.key)
				}

			default:
				sp.loggers.Infof("Unexpected event found in stream: %s", event.Event())
			}

			if processedEvent {
				sp.dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateValid, interfaces.DataSourceErrorInfo{})
			}
			if shouldRestart {
				stream.Restart()
			}

		case newStoreStatus := <-sp.storeStatusCh:
			if sp.loggers.IsDebugEnabled() {
				sp.loggers.Debugf("StreamProcessor received store status update: %+v", newStoreStatus)
			}
			if newStoreStatus.Available {
				// The store has just transitioned from unavailable to available (scenario 2a above)
				if newStoreStatus.NeedsRefresh {
					// The store is telling us that it can't guarantee that all of the latest data was cached.
					// So we'll restart the stream to ensure a full refresh.
					sp.loggers.Warn("Restarting stream to refresh data after feature store outage")
					stream.Restart()
				}
				// All of the updates were cached and have been written to the store, so we don't need to
				// restart the stream. We just need to make sure the client knows we're initialized now
				// (in case the initial "put" was not stored).
				sp.setInitializedAndNotifyClient(true, closeWhenReady)
			}

		case <-sp.halt:
			stream.Close()
			return
		}
	}
}

func (sp *StreamProcessor) subscribe(closeWhenReady chan<- struct{}) {
	req, _ := http.NewRequest("GET", sp.streamURI+"/all", nil)
	for k, vv := range sp.headers {
		req.Header[k] = vv
	}
	sp.loggers.Info("Connecting to LaunchDarkly stream")

	sp.logConnectionStarted()

	initialRetryDelay := sp.initialReconnectDelay
	if initialRetryDelay <= 0 {
		initialRetryDelay = defaultStreamRetryDelay
	}

	errorHandler := func(err error) es.StreamErrorHandlerResult {
		sp.logConnectionResult(false)

		if se, ok := err.(es.SubscriptionError); ok {
			errorInfo := interfaces.DataSourceErrorInfo{
				Kind:       interfaces.DataSourceErrorKindErrorResponse,
				StatusCode: se.Code,
				Time:       time.Now(),
			}
			recoverable := checkIfErrorIsRecoverableAndLog(
				sp.loggers,
				httpErrorDescription(se.Code),
				streamingErrorContext,
				se.Code,
				streamingWillRetryMessage,
			)
			if recoverable {
				sp.logConnectionStarted()
				sp.dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateInterrupted, errorInfo)
				return es.StreamErrorHandlerResult{CloseNow: false}
			}
			sp.dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateOff, errorInfo)
			return es.StreamErrorHandlerResult{CloseNow: true}
		}

		checkIfErrorIsRecoverableAndLog(
			sp.loggers,
			err.Error(),
			streamingErrorContext,
			0,
			streamingWillRetryMessage,
		)
		errorInfo := interfaces.DataSourceErrorInfo{
			Kind:    interfaces.DataSourceErrorKindNetworkError,
			Message: err.Error(),
			Time:    time.Now(),
		}
		sp.dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateInterrupted, errorInfo)
		sp.logConnectionStarted()
		return es.StreamErrorHandlerResult{CloseNow: false}
	}

	stream, err := es.SubscribeWithRequestAndOptions(req,
		es.StreamOptionHTTPClient(sp.client),
		es.StreamOptionReadTimeout(streamReadTimeout),
		es.StreamOptionInitialRetry(initialRetryDelay),
		es.StreamOptionUseBackoff(streamMaxRetryDelay),
		es.StreamOptionUseJitter(streamJitterRatio),
		es.StreamOptionRetryResetInterval(streamRetryResetInterval),
		es.StreamOptionErrorHandler(errorHandler),
		es.StreamOptionCanRetryFirstConnection(-1),
		es.StreamOptionLogger(sp.loggers.ForLevel(ldlog.Info)),
	)

	if err != nil {
		sp.logConnectionResult(false)

		close(closeWhenReady)
		return
	}

	sp.consumeStream(stream, closeWhenReady)
}

func (sp *StreamProcessor) setInitializedAndNotifyClient(success bool, closeWhenReady chan<- struct{}) {
	if success {
		sp.setInitializedOnce.Do(func() {
			sp.loggers.Info("LaunchDarkly streaming is active")
			sp.isInitialized = true
		})
	}
	sp.readyOnce.Do(func() {
		close(closeWhenReady)
	})
}

func (sp *StreamProcessor) logConnectionStarted() {
	sp.connectionAttemptLock.Lock()
	defer sp.connectionAttemptLock.Unlock()
	sp.connectionAttemptStartTime = ldtime.UnixMillisNow()
}

func (sp *StreamProcessor) logConnectionResult(success bool) {
	sp.connectionAttemptLock.Lock()
	startTimeWas := sp.connectionAttemptStartTime
	sp.connectionAttemptStartTime = 0
	sp.connectionAttemptLock.Unlock()

	if startTimeWas > 0 && sp.diagnosticsManager != nil {
		timestamp := ldtime.UnixMillisNow()
		sp.diagnosticsManager.RecordStreamInit(timestamp, !success, uint64(timestamp-startTimeWas))
	}
}

//nolint:golint,stylecheck // no doc comment for standard method
func (sp *StreamProcessor) Close() error {
	sp.closeOnce.Do(func() {
		sp.loggers.Info("Closing event stream")
		close(sp.halt)
		if sp.storeStatusCh != nil {
			sp.dataSourceUpdates.GetDataStoreStatusProvider().RemoveStatusListener(sp.storeStatusCh)
		}
		sp.dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateOff, interfaces.DataSourceErrorInfo{})
	})
	return nil
}

// GetBaseURI returns the configured streaming base URI, for testing.
func (sp *StreamProcessor) GetBaseURI() string {
	return sp.streamURI
}

// GetInitialReconnectDelay returns the configured reconnect delay, for testing.
func (sp *StreamProcessor) GetInitialReconnectDelay() time.Duration {
	return sp.initialReconnectDelay
}
