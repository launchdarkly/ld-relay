package bigsegments

import (
	"encoding/json"
	"fmt"
	"io"
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
	unboundedPollPath          = "/sdk/big-segments/revisions"
	unboundedStreamPath        = "/big-segments"
	streamReadTimeout          = 5 * time.Minute
	defaultStreamRetryInterval = 10 * time.Second
	synchronizedOnInterval     = 30 * time.Second

	segmentUpdatesChannelBufferSize = 20
)

// BigSegmentSynchronizer synchronizes big segment state for a given environment.
type BigSegmentSynchronizer interface {
	// Start begins synchronization of an environment.
	//
	// This method does not block.
	//
	// If the BigSegmentSynchronizer has already been started, or has been closed, this has no effect.
	Start()

	// HasSynced returns true if the synchronizer has ever successfully synced the data.
	//
	// We use this to determine whether Relay's internal SDK instances should bother trying to query
	// big segments metadata. If we haven't yet written any metadata, then trying to do so would
	// produce useless errors.
	HasSynced() bool

	// SegmentUpdatesCh returns a channel for notifications about segment data updates.
	//
	// Each value posted to this channel represents a batch of updates that the synchronizer has
	// applied. The caller is responsible for reading the channel to avoid blocking the
	// synchronizer.
	SegmentUpdatesCh() <-chan UpdatesSummary

	// Close ends synchronization of an environment.
	//
	// This method does not block.
	//
	// The BigSegmentSynchronizer cannot be restarted after calling Close.
	Close()
}

// UpdatesSummary describes a batch of updates that the synchronizer has applied.
type UpdatesSummary struct {
	// SegmentKeysUpdated is a slice of segment keys (plain keys as used by the SDK-- not segment
	// IDs, i.e. there is no generation suffix).
	SegmentKeysUpdated []string
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
	logPrefix string,
) BigSegmentSynchronizer

// defaultBigSegmentSynchronizer is the standard implementation of BigSegmentSynchronizer.
type defaultBigSegmentSynchronizer struct {
	httpConfig          httpconfig.HTTPConfig
	store               BigSegmentStore
	pollURI             string
	streamURI           string
	envID               config.EnvironmentID
	sdkKey              config.SDKKey
	streamRetryInterval time.Duration
	segmentUpdatesChan  chan UpdatesSummary
	hasSynced           bool
	syncedLock          sync.RWMutex
	startOnce           sync.Once
	closeChan           chan struct{}
	closeOnce           sync.Once
	loggers             ldlog.Loggers
}

type segmentChangesSummary map[string]struct{}

type applyPatchesResult struct {
	totalPatchesCount   int
	patchesAppliedCount int
	segmentsUpdated     segmentChangesSummary
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
	logPrefix string,
) BigSegmentSynchronizer {
	return newDefaultBigSegmentSynchronizer(httpConfig, store, pollURI, streamURI, envID, sdkKey, loggers, logPrefix)
}

func newDefaultBigSegmentSynchronizer(
	httpConfig httpconfig.HTTPConfig,
	store BigSegmentStore,
	pollURI string,
	streamURI string,
	envID config.EnvironmentID,
	sdkKey config.SDKKey,
	loggers ldlog.Loggers,
	logPrefix string,
) *defaultBigSegmentSynchronizer {
	s := defaultBigSegmentSynchronizer{
		httpConfig:          httpConfig,
		store:               store,
		pollURI:             strings.TrimSuffix(pollURI, "/") + unboundedPollPath,
		streamURI:           strings.TrimSuffix(streamURI, "/") + unboundedStreamPath,
		envID:               envID,
		sdkKey:              sdkKey,
		streamRetryInterval: defaultStreamRetryInterval,
		segmentUpdatesChan:  make(chan UpdatesSummary, segmentUpdatesChannelBufferSize),
		closeChan:           make(chan struct{}),
		loggers:             loggers,
	}

	if logPrefix != "" {
		logPrefix += " "
	}
	logPrefix += "BigSegmentSynchronizer:"
	s.loggers.SetPrefix(logPrefix)

	return &s
}

type httpStatusError struct {
	statusCode int
}

func (m httpStatusError) Error() string {
	return fmt.Sprintf("HTTP error %d", m.statusCode)
}

func (s *defaultBigSegmentSynchronizer) Start() {
	s.startOnce.Do(func() {
		go s.syncSupervisor()
	})
}

func (s *defaultBigSegmentSynchronizer) HasSynced() bool {
	s.syncedLock.RLock()
	ret := s.hasSynced
	s.syncedLock.RUnlock()
	return ret
}

func (s *defaultBigSegmentSynchronizer) SegmentUpdatesCh() <-chan UpdatesSummary {
	return s.segmentUpdatesChan
}

func (s *defaultBigSegmentSynchronizer) Close() {
	// If we haven't yet started, we still need to close the updates channel; calling
	// startOnce.Do also ensures that Start() will have no effect after this
	s.startOnce.Do(func() {
		close(s.segmentUpdatesChan)
	})
	// If we had already started, then there's a goroutine which will detect the closing
	// of closeChan, and that goroutine will take care of closing the updates channel
	s.closeOnce.Do(func() {
		close(s.closeChan)
	})
}

func (s *defaultBigSegmentSynchronizer) syncSupervisor() {
	isRetry := false
	for {
		err := s.sync(isRetry)
		if err != nil {
			s.loggers.Error("Synchronization failed:", err)
			if statusError, ok := err.(httpStatusError); ok {
				if !isHTTPErrorRecoverable(statusError.statusCode) {
					return
				}
			}
		}
		s.loggers.Warn("Will retry")
		timer := time.NewTimer(s.streamRetryInterval)
		defer timer.Stop()
		select {
		case <-s.closeChan:
			close(s.segmentUpdatesChan)
			return
		case <-timer.C:
		}
		isRetry = true
	}
}

func (s *defaultBigSegmentSynchronizer) sync(isRetry bool) error {
	s.loggers.Debug("Polling for big segment updates")
	segmentsUpdated := make(segmentChangesSummary)
	for {
	SyncLoop:
		for {
			select {
			case <-s.closeChan:
				return nil
			default:
				done, updates, err := s.poll()
				if err != nil {
					return err
				}
				if isRetry {
					s.loggers.Warn("Re-established connection")
					isRetry = false
				}
				segmentsUpdated.addAll(updates)
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

		done, updates, err := s.poll()
		if err != nil {
			return err
		}
		segmentsUpdated.addAll(updates)
		if !done {
			continue
		}

		s.loggers.Debug("Marking store as synchronized")
		err = s.setSynced()
		if err != nil {
			s.loggers.Error("Updating store timestamp failed:", err)
			return err
		}

		s.notifySegmentsUpdated(segmentsUpdated)

		return s.consumeStream(stream)
	}
}

func (s *defaultBigSegmentSynchronizer) setSynced() error {
	err := s.store.setSynchronizedOn(ldtime.UnixMillisNow())
	if err != nil {
		return err
	}
	s.syncedLock.Lock()
	s.hasSynced = true
	s.syncedLock.Unlock()
	return nil
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

func (s *defaultBigSegmentSynchronizer) poll() (bool, segmentChangesSummary, error) {
	client := s.httpConfig.Client()

	request, err := http.NewRequest("GET", s.pollURI, nil)
	if err != nil {
		return false, nil, err
	}

	request.Header.Set("Authorization", string(s.sdkKey))

	cursor, err := s.store.getCursor()
	if err != nil {
		return false, segmentChangesSummary{}, err
	}

	if cursor != "" {
		query := request.URL.Query()
		query.Add("after", cursor)
		request.URL.RawQuery = query.Encode()
	}

	s.loggers.Debugf("Polling %s", request.URL)
	response, err := client.Do(request)
	if err != nil {
		return false, segmentChangesSummary{}, err
	}
	defer response.Body.Close() //nolint:errcheck,gosec

	if response.StatusCode != 200 {
		return false, segmentChangesSummary{}, &httpStatusError{response.StatusCode}
	}

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return false, segmentChangesSummary{}, err
	}

	applyPatchResult, err := s.applyPatches(responseBody)

	return applyPatchResult.totalPatchesCount == 0, applyPatchResult.segmentsUpdated, err
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
		es.StreamOptionLogger(s.loggers.ForLevel(ldlog.Info)),
		es.StreamOptionErrorHandler(func(err error) es.StreamErrorHandlerResult {
			s.loggers.Warnf("Stream connection failed: %s", err)
			return es.StreamErrorHandlerResult{CloseNow: true}
		}),
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
			timer.Stop()
			if !ok {
				s.loggers.Debug("Stream ended")
				return nil
			}

			s.loggers.Debug("Received update(s) from stream")
			applyPatchResult, err := s.applyPatches([]byte(event.Data()))
			if err != nil {
				return err
			}
			s.notifySegmentsUpdated(applyPatchResult.segmentsUpdated)
			if applyPatchResult.patchesAppliedCount < applyPatchResult.totalPatchesCount {
				return nil // forces a restart if we got an out-of-order patch
			}

			if err := s.setSynced(); err != nil {
				return err
			}
		case <-timer.C:
			err := s.setSynced()
			if err != nil {
				return err
			}
		case <-s.closeChan:
			timer.Stop()
			return nil
		}
	}
}

// Returns total number of patches, number of patches applied, raw segment IDs, error
func (s *defaultBigSegmentSynchronizer) applyPatches(jsonData []byte) (applyPatchesResult, error) {
	var patches []bigSegmentPatch
	err := json.Unmarshal(jsonData, &patches)
	if err != nil {
		return applyPatchesResult{}, err
	}

	ret := applyPatchesResult{
		totalPatchesCount: len(patches),
		segmentsUpdated:   make(segmentChangesSummary),
	}
	for _, patch := range patches {
		if enableTraceLogging {
			s.loggers.Debugf("Received patch: %+v", patch)
		} else {
			s.loggers.Debugf("Received patch for version %q (from previous version %q)", patch.Version, patch.PreviousVersion)
		}
		success, err := s.store.applyPatch(patch)
		if err != nil {
			return ret, err
		}
		if !success {
			s.loggers.Warnf("Received a patch to previous version %q which was not the latest known version; skipping", patch.PreviousVersion)
			break
		}
		ret.patchesAppliedCount++
		ret.segmentsUpdated.addSegmentID(patch.SegmentID)
	}
	if ret.patchesAppliedCount > 0 {
		updatesDesc := "updates"
		if ret.patchesAppliedCount == 1 {
			updatesDesc = "update"
		}
		s.loggers.Infof("Applied %d %s", ret.patchesAppliedCount, updatesDesc)
	}
	return ret, nil
}

func (s *defaultBigSegmentSynchronizer) notifySegmentsUpdated(segmentsUpdated segmentChangesSummary) {
	keys := segmentsUpdated.getUpdatedSegmentKeys()
	if len(keys) != 0 {
		s.segmentUpdatesChan <- UpdatesSummary{SegmentKeysUpdated: keys}
	}
}

func segmentIDToSegmentKey(segmentID string) string {
	if p := strings.LastIndexByte(segmentID, '.'); p > 0 {
		return segmentID[0:p]
	}
	return segmentID
}

func (s *segmentChangesSummary) addSegmentID(segmentID string) {
	(*s)[segmentIDToSegmentKey(segmentID)] = struct{}{}
}

func (s *segmentChangesSummary) addAll(other segmentChangesSummary) {
	for key := range other {
		(*s)[key] = struct{}{}
	}
}

func (s *segmentChangesSummary) getUpdatedSegmentKeys() []string {
	if len(*s) == 0 {
		return nil
	}
	ret := make([]string, 0, len(*s))
	for key := range *s {
		ret = append(ret, key)
	}
	return ret
}
