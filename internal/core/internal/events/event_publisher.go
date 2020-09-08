package events

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
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

// EventPublisher is the interface for queueing and flushing proxied events.
type EventPublisher interface {
	// Publish adds any number of arbitrary JSON-serializable objects to the queue.
	Publish(...interface{})

	// PublishRaw adds any number of JSON elements to the queue.
	PublishRaw(...json.RawMessage)

	// Flush attempts to deliver all queued events.
	Flush()

	// ReplaceCredential changes the authorization credential used when sending events, if the previous
	// credential was of the same type.
	ReplaceCredential(config.SDKCredential)

	// Close releases all resources used by this object.
	Close()
}

// HTTPEventPublisher is the standard implementation of EventPublisher.
type HTTPEventPublisher struct {
	eventsURI   url.URL
	loggers     ldlog.Loggers
	sender      *reconfigurableEventSender
	authKey     config.SDKCredential
	baseHeaders http.Header
	closer      chan<- struct{}
	closeOnce   sync.Once
	wg          sync.WaitGroup
	inputQueue  chan interface{}
	queue       []interface{}
	capacity    int
	lock        sync.RWMutex
}

type batch []interface{}
type rawBatch []json.RawMessage
type flush struct{}

func (b batch) append(q *[]interface{}, max int, loggers *ldlog.Loggers, reachedCapacity *bool) {
	available := max - len(*q)
	taken := len(b)
	if available < len(b) {
		if !*reachedCapacity {
			loggers.Warnf("Exceeded event queue capacity of %d. Increase capacity to avoid dropping events.", max)
		}
		*reachedCapacity = true
		taken = available
	}
	*q = append(*q, b[:taken]...)
}

func (b rawBatch) append(q *[]interface{}, max int, loggers *ldlog.Loggers, reachedCapacity *bool) {
	available := max - len(*q)
	taken := len(b)
	if available < len(b) {
		if !*reachedCapacity {
			loggers.Warnf("Exceeded event queue capacity of %d. Increase capacity to avoid dropping events.", max)
		}
		*reachedCapacity = true
		taken = available
	}
	for _, e := range b[:taken] {
		*q = append(*q, e)
	}
}

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
	baseHeaders.Set("Authorization", authKey.GetAuthorizationHeaderValue())
	inputQueue := make(chan interface{}, inputQueueSize)
	p := &HTTPEventPublisher{
		baseHeaders: baseHeaders,
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
			return nil, err
		}
		if o, ok := o.(OptionFlushInterval); ok {
			if o > 0 {
				flushInterval = time.Duration(o)
			}
		}
	}

	p.sender = newReconfigurableEventSender(client, p.eventsURI.String(), baseHeaders, p.loggers)

	p.queue = make([]interface{}, 0, p.capacity)
	p.wg.Add(1)

	ticker := time.NewTicker(flushInterval)

	go func() {
		for {
			if err := recover(); err != nil {
				p.loggers.Errorf("Unexpected panic in event relay : %+v", err)
				continue
			}
			reachedCapacity := false
		EventLoop:
			for {
				select {
				case e := <-inputQueue:
					switch e := e.(type) {
					case flush:
						p.flush()
						continue EventLoop
					case rawBatch:
						e.append(&p.queue, p.capacity, &p.loggers, &reachedCapacity)
					case batch:
						e.append(&p.queue, p.capacity, &p.loggers, &reachedCapacity)
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

func (p *HTTPEventPublisher) ReplaceCredential(newCredential config.SDKCredential) { //nolint:golint // method is already documented in interface
	p.lock.Lock()
	if reflect.TypeOf(newCredential) != reflect.TypeOf(p.authKey) {
		p.lock.Unlock()
		return
	}
	p.authKey = newCredential
	p.lock.Unlock()
	p.sender.replaceCredential(newCredential)
}

func (p *HTTPEventPublisher) Publish(events ...interface{}) { //nolint:golint // method is already documented in interface
	p.inputQueue <- batch(events)
}

func (p *HTTPEventPublisher) PublishRaw(events ...json.RawMessage) { //nolint:golint // method is already documented in interface
	p.inputQueue <- rawBatch(events)
}

func (p *HTTPEventPublisher) Flush() { //nolint:golint // method is already documented in interface
	p.inputQueue <- flush{}
}

func (p *HTTPEventPublisher) flush() {
	count := len(p.queue)
	if count == 0 {
		return
	}
	payload, err := json.Marshal(p.queue[0:count])
	p.queue = make([]interface{}, 0, p.capacity)
	if err != nil {
		p.loggers.Errorf("Unexpected error marshalling event json: %+v", err)
		return
	}
	p.wg.Add(1)

	// We access the EventSender under lock because it may need to be replaced if the auth key changes
	p.lock.RLock()
	sender := p.sender
	p.lock.RUnlock()

	go func() {
		// The EventSender created by ldevents.NewDefaultEventSender implements the standard retry behavior,
		// and error logging, in its SendEventData method.
		result := sender.SendEventData(ldevents.AnalyticsEventDataKind, payload, count)
		p.wg.Done()
		if result.MustShutDown {
			p.Close()
		}
	}()
}

func (p *HTTPEventPublisher) Close() { //nolint:golint // method is already documented in interface
	p.closeOnce.Do(func() {
		close(p.closer)
		p.wg.Wait()
	})
}
