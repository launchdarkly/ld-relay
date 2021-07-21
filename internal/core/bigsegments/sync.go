package bigsegments

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"

	es "github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
)

const (
	unboundedPollPath      = "/sdk/big-segments/revisions"
	unboundedStreamPath    = "/big-segments"
	streamReadTimeout      = 5 * time.Minute
	retryInterval          = 10 * time.Second
	synchronizedOnInterval = 30 * time.Second
)

// BigSegmentSynchronizer synchronizes big segment state for a given environment.
type BigSegmentSynchronizer interface {
	// Start begins synchronization of an environment.
	//
	// This method does not block.
	Start()

	// Close ends synchronization of an evironment.
	//
	// This method does not block.
	Close()
}

// BigSegmentSynchronizerFactory creates an implementation of BigSegmentSynchronizer. We
// only use a single implementation in real life, but this allows us to use a mock one
// in tests. Calling the factory does not automatically start the synchronizer.
type BigSegmentSynchronizerFactory func(
	httpConfig httpconfig.HTTPConfig,
	store BigSegmentStore,
	pollURI string,
	streamURI string,
	envID config.EnvironmentID,
	sdkKey config.SDKKey,
	loggers ldlog.Loggers,
) BigSegmentSynchronizer

// defaultBigSegmentSynchronizer is the standard implementation of BigSegmentSynchronizer.
type defaultBigSegmentSynchronizer struct {
	httpConfig httpconfig.HTTPConfig
	store      BigSegmentStore
	pollURI    string
	streamURI  string
	envID      config.EnvironmentID
	sdkKey     config.SDKKey
	closeChan  chan struct{}
	closeOnce  sync.Once
	loggers    ldlog.Loggers
}

// DefaultBigSegmentSynchronizerFactory creates the default implementation of BigSegmentSynchronizer.
func DefaultBigSegmentSynchronizerFactory(
	httpConfig httpconfig.HTTPConfig,
	store BigSegmentStore,
	pollURI string,
	streamURI string,
	envID config.EnvironmentID,
	sdkKey config.SDKKey,
	loggers ldlog.Loggers,
) BigSegmentSynchronizer {
	return newDefaultBigSegmentSynchronizer(httpConfig, store, pollURI, streamURI, envID, sdkKey, loggers)
}

func newDefaultBigSegmentSynchronizer(
	httpConfig httpconfig.HTTPConfig,
	store BigSegmentStore,
	pollURI string,
	streamURI string,
	envID config.EnvironmentID,
	sdkKey config.SDKKey,
	loggers ldlog.Loggers,
) *defaultBigSegmentSynchronizer {
	s := defaultBigSegmentSynchronizer{
		httpConfig: httpConfig,
		store:      store,
		pollURI:    strings.TrimSuffix(pollURI, "/") + unboundedPollPath,
		streamURI:  strings.TrimSuffix(streamURI, "/") + unboundedStreamPath,
		envID:      envID,
		sdkKey:     sdkKey,
		closeChan:  make(chan struct{}),
		loggers:    loggers,
	}

	s.loggers.SetPrefix("BigSegmentSynchronizer:")

	return &s
}

type httpStatusError struct {
	statusCode int
}

func (m httpStatusError) Error() string {
	return fmt.Sprintf("HTTP error %d", m.statusCode)
}

func (s *defaultBigSegmentSynchronizer) Start() {
	go s.syncSupervisor()
}

func (s *defaultBigSegmentSynchronizer) Close() {
	s.closeOnce.Do(func() {
		close(s.closeChan)
	})
}

func (s *defaultBigSegmentSynchronizer) syncSupervisor() {
	for {
		timer := time.NewTimer(retryInterval)
		err := s.sync()
		if err != nil {
			s.loggers.Error("Synchronization failed:", err)
			if statusError, ok := err.(httpStatusError); ok {
				if !isHTTPErrorRecoverable(statusError.statusCode) {
					return
				}
			}
		}
		select {
		case <-s.closeChan:
			return
		case <-timer.C:
		}
	}
}

func (s *defaultBigSegmentSynchronizer) sync() error {
	s.loggers.Debug("Polling for big segment updates")
	for {
	SyncLoop:
		for {
			select {
			case <-s.closeChan:
				return nil
			default:
				done, err := s.poll()
				if err != nil {
					return err
				}
				if done {
					break SyncLoop
				}
			}
		}

		stream, err := s.connectStream()
		if err != nil {
			return err
		}
		defer stream.Close()

		done, err := s.poll()
		if err != nil {
			return err
		}
		if !done {
			continue
		}

		err = s.store.setSynchronizedOn(ldtime.UnixMillisNow())
		if err != nil {
			s.loggers.Error("Updating store timestamp failed:", err)
			return err
		}

		return s.consumeStream(stream)
	}
}

// Tests whether an HTTP error status represents a condition that might resolve
// on its own if we retry, or at least should not make us permanently stop
// sending requests.
func isHTTPErrorRecoverable(statusCode int) bool {
	if statusCode >= 400 && statusCode < 500 {
		switch statusCode {
		case 400: // bad request
			return true
		case 408: // request timeout
			return true
		case 429: // too many requests
			return true
		default:
			return false // all other 4xx errors are unrecoverable
		}
	}
	return true
}

func (s *defaultBigSegmentSynchronizer) poll() (bool, error) {
	client := s.httpConfig.Client()

	request, err := http.NewRequest("GET", s.pollURI, nil)
	if err != nil {
		return false, err
	}

	request.Header.Set("Authorization", string(s.sdkKey))

	cursor, err := s.store.getCursor()
	if err != nil {
		return false, err
	}

	if cursor != "" {
		query := request.URL.Query()
		query.Add("after", cursor)
		request.URL.RawQuery = query.Encode()
	}

	s.loggers.Debugf("Polling %s", request.URL)
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close() //nolint:errcheck

	if response.StatusCode != 200 {
		return false, &httpStatusError{response.StatusCode}
	}

	responseBody, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return false, err
	}

	totalCount, _, err := s.applyPatches(responseBody)

	return totalCount == 0, err
}

func (s *defaultBigSegmentSynchronizer) connectStream() (*es.Stream, error) {
	s.loggers.Debugf("Making stream request to %s", s.streamURI)
	request, err := http.NewRequest("GET", s.streamURI, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("Authorization", string(s.sdkKey))

	client := s.httpConfig.Client()
	client.Timeout = 0

	stream, err := es.SubscribeWithRequestAndOptions(request,
		es.StreamOptionHTTPClient(client),
		es.StreamOptionReadTimeout(streamReadTimeout),
	)
	if err != nil {
		if se, ok := err.(es.SubscriptionError); ok {
			return nil, &httpStatusError{se.Code}
		}
		return nil, err
	}

	return stream, nil
}

func (s *defaultBigSegmentSynchronizer) consumeStream(stream *es.Stream) error {
	for {
		timer := time.NewTimer(synchronizedOnInterval)
		select {
		case event, ok := <-stream.Events:
			if !ok {
				return nil
			}

			s.loggers.Debug("Received update(s) from stream")
			totalCount, appliedCount, err := s.applyPatches([]byte(event.Data()))
			if err != nil {
				return err
			}
			if appliedCount < totalCount {
				return nil // forces a restart if we got an out-of-order patch
			}

			if err := s.store.setSynchronizedOn(ldtime.UnixMillisNow()); err != nil {
				return err
			}
		case <-timer.C:
			err := s.store.setSynchronizedOn(ldtime.UnixMillisNow())
			if err != nil {
				return err
			}
		case <-s.closeChan:
			return nil
		}
	}
}

// Returns total number of patches, number of patches applied, error
func (s *defaultBigSegmentSynchronizer) applyPatches(jsonData []byte) (int, int, error) {
	var patches []bigSegmentPatch
	err := json.Unmarshal(jsonData, &patches)
	if err != nil {
		return 0, 0, err
	}

	successCount := 0
	for _, patch := range patches {
		s.loggers.Debugf("Received patch for version %q (from previous version %q)", patch.Version, patch.PreviousVersion)
		success, err := s.store.applyPatch(patch)
		if err != nil {
			return len(patches), successCount, err
		}
		if !success {
			s.loggers.Warnf("Received a patch to previous version %q which was not the latest known version; skipping", patch.PreviousVersion)
			break
		}
		successCount++
	}
	if successCount > 0 {
		s.loggers.Infof("Applied %d updates", successCount)
	}
	return len(patches), successCount, nil
}
