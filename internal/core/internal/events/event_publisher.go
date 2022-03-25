package events

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
)

const (
	defaultFlushInterval = time.Minute
	defaultCapacity      = 1000
	inputQueueSize       = 100
	defaultEventsURIPath = "/bulk"
)

var (
	defaultEventsEndpointURI, _ = url.Parse("https://events.launchdarkly.com/bulk") //nolint:gochecknoglobals
)

// EventPublisher is the interface for the component that buffers events and delivers them to LaunchDarkly.
// Events are treated as raw JSON data; the component does not do any parsing or transformation of them.
//
// A single instance of the component exists for each environment, associated with a single credential (such
// as an SDK key). However, it can maintain multiple buffers if it is necessary to deliver events in
// separate batches due to SDKs providing different header metadata, as represented by EventPublisherContext.
//
// The only implementation of this in Relay is HTTPEventPublisher. It is an interface only so that it can
// be mocked in test code.
type EventPublisher interface {
	// Publish adds any number of JSON elements to the queue.
	//
	// The EventPayloadMetadata value provides a way to distinguish between batches of events that have
	// different header metadata. If no such distinction is needed, it can simply be an empty
	// EventPayloadMetadata{}. Otherwise, each distinct value of EventPayloadMetadata gets its own event
	// queue, all of which will be flushed at the same time but delivered in separate HTTP posts.
	Publish(EventPayloadMetadata, ...json.RawMessage)

	// Flush attempts to deliver all queued events.
	Flush()

	// ReplaceCredential changes the authorization credential used when sending events, if the previous
	// credential was of the same type.
	ReplaceCredential(config.SDKCredential)

	// Close releases all resources used by this object.
	Close()
}

// EventPayloadMetadata represents HTTP header metadata that may be included in an event post from an SDK, which
// Relay should copy when it forwards the events to LaunchDarkly.
type EventPayloadMetadata struct {
	// SchemaVersion is the numeric value of the X-LaunchDarkly-Event-Schema header, or 1 if unknown
	// (in version 1, this header was not used).
	SchemaVersion int
	// Tags is the value of the X-LaunchDarkly-Tags header, or "" if none.
	Tags string
}

// GetEventPayloadMetadata parses EventPayloadMetadata values from an HTTP request.
func GetEventPayloadMetadata(req *http.Request) EventPayloadMetadata {
	ret := EventPayloadMetadata{
		Tags: req.Header.Get(TagsHeader),
	}
	ret.SchemaVersion, _ = strconv.Atoi(req.Header.Get(EventSchemaHeader))
	if ret.SchemaVersion <= 0 {
		ret.SchemaVersion = 1
	}
	return ret
}

// HTTPEventPublisher is the standard implementation of EventPublisher.
type HTTPEventPublisher struct {
	eventsURI   url.URL
	loggers     ldlog.Loggers
	client      *http.Client
	authKey     config.SDKCredential
	baseHeaders http.Header
	closer      chan<- struct{}
	closeOnce   sync.Once
	wg          sync.WaitGroup
	inputQueue  chan interface{}

	queues     map[EventPayloadMetadata]*publisherQueue
	capacity   int
	overflowed bool
	lock       sync.RWMutex
}

type eventBatch struct {
	metadata EventPayloadMetadata
	events   []json.RawMessage
}

type publisherQueue struct {
	events []json.RawMessage
}

type flush struct{}

// OptionType defines optional parameters for NewHTTPEventPublisher.
type OptionType interface {
	apply(*HTTPEventPublisher) error
}

// OptionURI specifies a custom base URI for the events service.
type OptionURI string

func (o OptionURI) apply(p *HTTPEventPublisher) error { //nolint:unparam
	u, err := url.Parse(strings.TrimRight(string(o), "/") + defaultEventsURIPath)
	if err == nil {
		p.eventsURI = *u
	}
	return nil
}

// OptionEndpointURI specifies a complete custom URI for the events service (not a base URI).
type OptionEndpointURI string

func (o OptionEndpointURI) apply(p *HTTPEventPublisher) error {
	u, err := url.Parse(string(o))
	if err == nil {
		p.eventsURI = *u
	}
	return err
}

// OptionFlushInterval specifies the interval for automatic flushes.
type OptionFlushInterval time.Duration

func (o OptionFlushInterval) apply(p *HTTPEventPublisher) error {
	return nil
}

// OptionCapacity specifies the event queue capacity.
type OptionCapacity int

func (o OptionCapacity) apply(p *HTTPEventPublisher) error {
	p.capacity = int(o)
	return nil
}

// NewHTTPEventPublisher creates a new HTTPEventPublisher.
func NewHTTPEventPublisher(authKey config.SDKCredential, httpConfig httpconfig.HTTPConfig, loggers ldlog.Loggers, options ...OptionType) (*HTTPEventPublisher, error) {
	closer := make(chan struct{})

	client := httpConfig.Client()
	baseHeaders := httpConfig.SDKHTTPConfig.GetDefaultHeaders()
	baseHeaders.Del("Authorization") // we don't necessarily want an SDK key here - we'll decide in makeEventSender()
	inputQueue := make(chan interface{}, inputQueueSize)
	p := &HTTPEventPublisher{
		baseHeaders: baseHeaders,
		client:      client,
		eventsURI:   *defaultEventsEndpointURI,
		authKey:     authKey,
		closer:      closer,
		capacity:    defaultCapacity,
		inputQueue:  inputQueue,
		loggers:     loggers,
	}

	flushInterval := defaultFlushInterval

	for _, o := range options {
		err := o.apply(p)
		if err != nil {
			return nil, err // COVERAGE: can't happen in unit tests
		}
		if o, ok := o.(OptionFlushInterval); ok {
			if o > 0 {
				flushInterval = time.Duration(o)
			}
		}
	}

	p.queues = make(map[EventPayloadMetadata]*publisherQueue)
	p.wg.Add(1)

	ticker := time.NewTicker(flushInterval)

	go func() {
		for {
			if err := recover(); err != nil { // COVERAGE: can't happen in unit tests
				p.loggers.Errorf("Unexpected panic in event relay : %+v", err)
				continue
			}
		EventLoop:
			for {
				select {
				case e := <-inputQueue:
					switch e := e.(type) {
					case flush:
						p.flush()
					case eventBatch:
						p.append(e)
					}
				case <-ticker.C:
					p.flush()
				case <-closer:
					break EventLoop
				}
			}
			ticker.Stop()
			p.wg.Done()
			break
		}
	}()

	return p, nil
}

func (p *HTTPEventPublisher) append(batch eventBatch) {
	queue := p.queues[batch.metadata]
	if queue == nil {
		queue = &publisherQueue{events: make([]json.RawMessage, 0, p.capacity)}
		p.queues[batch.metadata] = queue
	}
	available := p.capacity - len(queue.events)
	taken := len(batch.events)
	if available < len(batch.events) {
		if !p.overflowed {
			p.loggers.Warnf("Exceeded event queue capacity of %d. Increase capacity to avoid dropping events.", p.capacity)
			p.overflowed = true
		}
		taken = available
	} else {
		p.overflowed = false
	}
	queue.events = append(queue.events, batch.events[:taken]...)
}

func (p *HTTPEventPublisher) ReplaceCredential(newCredential config.SDKCredential) { //nolint:golint // method is already documented in interface
	p.lock.Lock()
	if reflect.TypeOf(newCredential) == reflect.TypeOf(p.authKey) {
		p.authKey = newCredential
	}
	p.lock.Unlock()
}

func (p *HTTPEventPublisher) Publish(metadata EventPayloadMetadata, events ...json.RawMessage) { //nolint:golint // method is already documented in interface
	p.inputQueue <- eventBatch{metadata, events}
}

func (p *HTTPEventPublisher) Flush() { //nolint:golint // method is already documented in interface
	p.inputQueue <- flush{}
}

func (p *HTTPEventPublisher) flush() {
	// Notes on implementation of this method:
	// - We are creating a new ldevents.EventSender for each payload delivery, because potentially
	// each one could have different headers (based on EventPayloadMetadata) and also because the
	// authorization key could change at any time. See comment on makeEventSender().
	// - In the common case where we do *not* receive events with multiple distinct EventsMetadata
	// values, we can save a tiny bit of overhead by reusing a single buffer. But if there are
	// multiple values (and therefore multiple queues), we don't want to keep accumulating buffers
	// that are never deallocated just because we received different metadata at some point. So in
	// the multiple-queue case, we will discard any buffers that haven't been used since last flush.
	if len(p.queues) == 0 {
		return
	}
	queues := p.queues
	discardingUnusedBuffers := false
	if len(p.queues) > 1 {
		// Recreate the map - we will re-add only the used buffers to it
		p.queues = make(map[EventPayloadMetadata]*publisherQueue)
		discardingUnusedBuffers = true
	}

	// We access p.authKey under lock because it can change
	p.lock.RLock()
	authKey := p.authKey
	p.lock.RUnlock()

	for metadata, queue := range queues {
		count := len(queue.events)
		if count == 0 {
			continue
		}
		payload, err := json.Marshal(queue.events)
		queue.events = queue.events[0:0]
		if discardingUnusedBuffers {
			p.queues[metadata] = queue
		}
		if err != nil { // COVERAGE: can't happen in unit tests
			p.loggers.Errorf("Unexpected error marshalling event json: %+v", err)
			continue
		}
		p.wg.Add(1)

		sender := makeEventSender(
			p.client,
			p.eventsURI.String(),
			p.baseHeaders,
			authKey,
			metadata,
			p.loggers,
		)

		go func() {
			// The EventSender created by ldevents.NewDefaultEventSender implements the standard retry behavior,
			// and error logging, in its SendEventData method. Retries could cause this call to block for a while,
			// so it's run on a separate goroutine.
			result := sender.SendEventData(ldevents.AnalyticsEventDataKind, payload, count)
			p.wg.Done()
			if result.MustShutDown {
				p.Close()
			}
		}()
	}
}

func (p *HTTPEventPublisher) Close() { //nolint:golint // method is already documented in interface
	p.closeOnce.Do(func() {
		close(p.closer)
		p.wg.Wait()
	})
}

// makeEventSender creates a new instance of the EventSender component that is provided by go-sdk-events,
// configuring it to have the appropriate HTTP request headers.
//
// This component provides the standard behavior for error handling and retries on event posts. It does
// not create its own goroutine or HTTP client or do any computations other than the minimum needed to
// send each request, so there's not much overhead to creating and disposing of instances. And since the
// current implementation doesn't allow the configured headers to be changed on a per-request basis, it's
// simplest for us to just create a new instance if the relevant configuration may have changed.
func makeEventSender(
	httpClient *http.Client,
	eventsURI string,
	baseHeaders http.Header,
	authKey config.SDKCredential,
	metadata EventPayloadMetadata,
	loggers ldlog.Loggers,
) ldevents.EventSender {
	headers := make(http.Header)
	for k, v := range baseHeaders {
		headers[k] = v
	}
	if authHeader := authKey.GetAuthorizationHeaderValue(); authHeader != "" {
		headers.Set("Authorization", authHeader)
	}
	if metadata.Tags != "" {
		headers.Set(TagsHeader, metadata.Tags)
	}
	return ldevents.NewDefaultEventSender(httpClient, eventsURI, "", headers, loggers)
}
