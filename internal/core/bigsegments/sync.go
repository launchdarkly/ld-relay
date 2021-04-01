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
)

const (
	unboundedPollPath      = "/relay/unbounded-segments/revisions"
	unboundedStreamPath    = "/unbounded-segments"
	streamReadTimeout      = 5 * time.Minute
	retryInterval          = 10 * time.Second
	synchronizedOnInterval = 30 * time.Second
)

// BigSegmentSynchronizer synchronizes big segment state for a given environment.
type BigSegmentSynchronizer struct {
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

// NewBigSegmentSynchronizer creates a big segment synchronizer for a given environment.
//
// The synchronizer is not automatically started.
func NewBigSegmentSynchronizer(
	httpConfig httpconfig.HTTPConfig,
	store BigSegmentStore,
	pollURI string,
	streamURI string,
	envID config.EnvironmentID,
	sdkKey config.SDKKey,
	loggers ldlog.Loggers,
) (*BigSegmentSynchronizer, error) {
	s := BigSegmentSynchronizer{
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

	return &s, nil
}

type httpStatusError struct {
	statusCode int
}

func (m httpStatusError) Error() string {
	return fmt.Sprintf("HTTP error %d", m.statusCode)
}

// Start begins synchronization of an environment.
//
// This method does not block.
func (s *BigSegmentSynchronizer) Start() {
	go s.syncSupervisor()
}

// Close ends synchronization of an evironment.
//
// This method does not block.
func (s *BigSegmentSynchronizer) Close() {
	s.closeOnce.Do(func() {
		close(s.closeChan)
	})
}

func (s *BigSegmentSynchronizer) syncSupervisor() {
	for {
		timer := time.NewTimer(retryInterval)
		err := s.sync()
		if err != nil {
			s.loggers.Error("synchronization failed:", err)
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

func (s *BigSegmentSynchronizer) sync() error {
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

		err = s.store.setSynchronizedOn(string(s.envID), time.Now())
		if err != nil {
			s.loggers.Error("updating store timestamp failed:", err)
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

func (s *BigSegmentSynchronizer) poll() (bool, error) {
	client := s.httpConfig.Client()

	request, err := http.NewRequest("GET", s.pollURI, nil)
	if err != nil {
		return false, err
	}

	request.Header.Set("Authorization", string(s.sdkKey))

	cursor, err := s.store.getCursor(string(s.envID))
	if err != nil {
		return false, err
	}

	if cursor != "" {
		query := request.URL.Query()
		query.Add("after", cursor)
		request.URL.RawQuery = query.Encode()
	}

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

	var patches []bigSegmentPatch
	err = json.Unmarshal(responseBody, &patches)
	if err != nil {
		return false, err
	}

	for _, patch := range patches {
		err = s.store.applyPatch(patch)
		if err != nil {
			return false, err
		}
	}

	return len(patches) == 0, nil
}

func (s *BigSegmentSynchronizer) connectStream() (*es.Stream, error) {
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

func (s *BigSegmentSynchronizer) consumeStream(stream *es.Stream) error {
	for {
		timer := time.NewTimer(synchronizedOnInterval)
		select {
		case event, ok := <-stream.Events:
			if !ok {
				return nil
			}

			var patches []bigSegmentPatch
			err := json.Unmarshal([]byte(event.Data()), &patches)
			if err != nil {
				return err
			}

			for _, patch := range patches {
				err = s.store.applyPatch(patch)
				if err != nil {
					return err
				}
			}

			err = s.store.setSynchronizedOn(string(s.envID), time.Now())
			if err != nil {
				return err
			}
		case <-timer.C:
			err := s.store.setSynchronizedOn(string(s.envID), time.Now())
			if err != nil {
				return err
			}
		case <-s.closeChan:
			return nil
		}
	}
}
