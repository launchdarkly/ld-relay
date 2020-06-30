package ldevents

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
)

const (
	defaultEventsURI   = "https://events.launchdarkly.com"
	eventSchemaHeader  = "X-LaunchDarkly-Event-Schema"
	payloadIDHeader    = "X-LaunchDarkly-Payload-ID"
	currentEventSchema = "3"
)

type defaultEventSender struct {
	httpClient    *http.Client
	eventsURI     string
	diagnosticURI string
	headers       http.Header
	loggers       ldlog.Loggers
	retryDelay    time.Duration
}

// NewDefaultEventSender creates the default implementation of EventSender.
func NewDefaultEventSender(
	httpClient *http.Client,
	eventsURI string,
	diagnosticURI string,
	headers http.Header,
	loggers ldlog.Loggers,
) EventSender {
	return &defaultEventSender{httpClient, eventsURI, diagnosticURI, headers, loggers, 0}
}

// NewServerSideEventSender creates the standard implementation of EventSender for server-side SDKs.
//
// This is a convenience function for calling NewDefaultEventSender with the standard event endpoint URIs and the
// Authorization header.
func NewServerSideEventSender(
	httpClient *http.Client,
	sdkKey string,
	eventsURI string,
	headers http.Header,
	loggers ldlog.Loggers,
) EventSender {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	allHeaders := make(http.Header)
	for k, vv := range headers {
		allHeaders[k] = vv
	}
	allHeaders.Set("Authorization", sdkKey)
	if eventsURI == "" {
		eventsURI = defaultEventsURI
	}
	return &defaultEventSender{
		httpClient:    httpClient,
		eventsURI:     strings.TrimRight(eventsURI, "/") + "/bulk",
		diagnosticURI: strings.TrimRight(eventsURI, "/") + "/diagnostic",
		headers:       allHeaders,
		loggers:       loggers,
	}
}

func (s *defaultEventSender) SendEventData(kind EventDataKind, data []byte, eventCount int) EventSenderResult {
	headers := make(http.Header)
	for k, vv := range s.headers {
		headers[k] = vv
	}
	headers.Set("Content-Type", "application/json")

	var uri string
	var description string

	switch kind {
	case AnalyticsEventDataKind:
		uri = s.eventsURI
		description = "diagnostic event"
		headers.Add(eventSchemaHeader, currentEventSchema)
		payloadUUID, _ := uuid.NewRandom()
		headers.Add(payloadIDHeader, payloadUUID.String())
		// if NewRandom somehow failed, we'll just proceed with an empty string
	case DiagnosticEventDataKind:
		uri = s.diagnosticURI
		description = fmt.Sprintf("%d events", eventCount)
	default:
		return EventSenderResult{}
	}

	s.loggers.Debugf("Sending %s: %s", description, data)

	var resp *http.Response
	var respErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			delay := s.retryDelay
			if delay == 0 {
				delay = time.Second
			}
			s.loggers.Warnf("Will retry posting events after %f second", delay/time.Second)
			time.Sleep(delay)
		}
		req, reqErr := http.NewRequest("POST", uri, bytes.NewReader(data))
		if reqErr != nil {
			s.loggers.Errorf("Unexpected error while creating event request: %+v", reqErr)
			return EventSenderResult{}
		}
		req.Header = headers

		resp, respErr = s.httpClient.Do(req)

		if resp != nil && resp.Body != nil {
			_, _ = ioutil.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}

		if respErr != nil {
			s.loggers.Warnf("Unexpected error while sending events: %+v", respErr)
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			result := EventSenderResult{Success: true}
			t, err := http.ParseTime(resp.Header.Get("Date"))
			if err == nil {
				result.TimeFromServer = ldtime.UnixMillisFromTime(t)
			}
			return result
		}
		if isHTTPErrorRecoverable(resp.StatusCode) {
			maybeRetry := "will retry"
			if attempt == 1 {
				maybeRetry = "some events were dropped"
			}
			s.loggers.Warnf(httpErrorMessage(resp.StatusCode, "sending events", maybeRetry))
		} else {
			s.loggers.Warnf(httpErrorMessage(resp.StatusCode, "sending events", ""))
			return EventSenderResult{MustShutDown: true}
		}
	}
	return EventSenderResult{}
}
