package ldclient

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"
)

// EventProcessor defines the interface for dispatching analytics events.
type EventProcessor interface {
	// SendEvent records an event asynchronously.
	SendEvent(Event)
	// Flush specifies that any buffered events should be sent as soon as possible, rather than waiting
	// for the next flush interval. This method is asynchronous, so events still may not be sent
	// until a later time.
	Flush()
	// Close shuts down all event processor activity, after first ensuring that all events have been
	// delivered. Subsequent calls to SendEvent() or Flush() will be ignored.
	Close() error
}

type nullEventProcessor struct{}

type defaultEventProcessor struct {
	inboxCh       chan eventDispatcherMessage
	inboxFullOnce sync.Once
	closeOnce     sync.Once
	loggers       ldlog.Loggers
}

type eventDispatcher struct {
	sdkKey            string
	config            Config
	lastKnownPastTime uint64
	disabled          bool
	stateLock         sync.Mutex
}

type eventBuffer struct {
	events           []Event
	summarizer       eventSummarizer
	capacity         int
	capacityExceeded bool
	loggers          ldlog.Loggers
}

type flushPayload struct {
	events  []Event
	summary eventSummary
}

type sendEventsTask struct {
	client    *http.Client
	eventsURI string
	loggers   ldlog.Loggers
	sdkKey    string
	userAgent string
	formatter eventOutputFormatter
}

// Payload of the inboxCh channel.
type eventDispatcherMessage interface{}

type sendEventMessage struct {
	event Event
}

type flushEventsMessage struct{}

type shutdownEventsMessage struct {
	replyCh chan struct{}
}

type syncEventsMessage struct {
	replyCh chan struct{}
}

const (
	maxFlushWorkers    = 5
	eventSchemaHeader  = "X-LaunchDarkly-Event-Schema"
	currentEventSchema = "3"
	defaultURIPath     = "/bulk"
)

func newNullEventProcessor() *nullEventProcessor {
	return &nullEventProcessor{}
}

func (n *nullEventProcessor) SendEvent(e Event) {}

func (n *nullEventProcessor) Flush() {}

func (n *nullEventProcessor) Close() error {
	return nil
}

// NewDefaultEventProcessor creates an instance of the default implementation of analytics event processing.
// This is normally only used internally; it is public because the Go SDK code is reused by other LaunchDarkly
// components.
func NewDefaultEventProcessor(sdkKey string, config Config, client *http.Client) EventProcessor {
	if client == nil {
		client = config.newHTTPClient()
	}
	inboxCh := make(chan eventDispatcherMessage, config.Capacity)
	startEventDispatcher(sdkKey, config, client, inboxCh)
	if config.SamplingInterval > 0 {
		config.Loggers.Warn("Config.SamplingInterval is deprecated")
	}
	return &defaultEventProcessor{
		inboxCh: inboxCh,
		loggers: config.Loggers,
	}
}

func (ep *defaultEventProcessor) SendEvent(e Event) {
	ep.postNonBlockingMessageToInbox(sendEventMessage{event: e})
}

func (ep *defaultEventProcessor) Flush() {
	ep.postNonBlockingMessageToInbox(flushEventsMessage{})
}

func (ep *defaultEventProcessor) postNonBlockingMessageToInbox(e eventDispatcherMessage) bool {
	select {
	case ep.inboxCh <- e:
		return true
	default:
	}
	// If the inbox is full, it means the eventDispatcher is seriously backed up with not-yet-processed events.
	// This is unlikely, but if it happens, it means the application is probably doing a ton of flag evaluations
	// across many goroutines-- so if we wait for a space in the inbox, we risk a very serious slowdown of the
	// app. To avoid that, we'll just drop the event. The log warning about this will only be shown once.
	ep.inboxFullOnce.Do(func() {
		ep.loggers.Warn("Events are being produced faster than they can be processed; some events will be dropped")
	})
	return false
}

func (ep *defaultEventProcessor) Close() error {
	ep.closeOnce.Do(func() {
		// We put the flush and shutdown messages directly into the channel instead of calling
		// postNonBlockingMessageToInbox, because we *do* want to block to make sure there is room in the channel;
		// these aren't analytics events, they are messages that are necessary for an orderly shutdown.
		ep.inboxCh <- flushEventsMessage{}
		m := shutdownEventsMessage{replyCh: make(chan struct{})}
		ep.inboxCh <- m
		<-m.replyCh
	})
	return nil
}

func startEventDispatcher(sdkKey string, config Config, client *http.Client,
	inboxCh <-chan eventDispatcherMessage) {
	ed := &eventDispatcher{
		sdkKey: sdkKey,
		config: config,
	}

	// Start a fixed-size pool of workers that wait on flushTriggerCh. This is the
	// maximum number of flushes we can do concurrently.
	flushCh := make(chan *flushPayload, 1)
	var workersGroup sync.WaitGroup
	for i := 0; i < maxFlushWorkers; i++ {
		startFlushTask(sdkKey, config, client, flushCh, &workersGroup,
			func(r *http.Response) { ed.handleResponse(r) })
	}
	go ed.runMainLoop(inboxCh, flushCh, &workersGroup)
}

func (ed *eventDispatcher) runMainLoop(inboxCh <-chan eventDispatcherMessage,
	flushCh chan<- *flushPayload, workersGroup *sync.WaitGroup) {
	if err := recover(); err != nil {
		ed.config.Loggers.Errorf("Unexpected panic in event processing thread: %+v", err)
	}

	outbox := eventBuffer{
		events:     make([]Event, 0, ed.config.Capacity),
		summarizer: newEventSummarizer(),
		capacity:   ed.config.Capacity,
		loggers:    ed.config.Loggers,
	}
	userKeys := newLruCache(ed.config.UserKeysCapacity)

	flushInterval := ed.config.FlushInterval
	if flushInterval <= 0 {
		flushInterval = DefaultConfig.FlushInterval
	}
	userKeysFlushInterval := ed.config.UserKeysFlushInterval
	if userKeysFlushInterval <= 0 {
		userKeysFlushInterval = DefaultConfig.UserKeysFlushInterval
	}
	flushTicker := time.NewTicker(flushInterval)
	usersResetTicker := time.NewTicker(userKeysFlushInterval)

	for {
		// Drain the response channel with a higher priority than anything else
		// to ensure that the flush workers don't get blocked.
		select {
		case message := <-inboxCh:
			switch m := message.(type) {
			case sendEventMessage:
				ed.processEvent(m.event, &outbox, &userKeys)
			case flushEventsMessage:
				ed.triggerFlush(&outbox, flushCh, workersGroup)
			case syncEventsMessage:
				workersGroup.Wait()
				m.replyCh <- struct{}{}
			case shutdownEventsMessage:
				flushTicker.Stop()
				usersResetTicker.Stop()
				workersGroup.Wait() // Wait for all in-progress flushes to complete
				close(flushCh)      // Causes all idle flush workers to terminate
				m.replyCh <- struct{}{}
				return
			}
		case <-flushTicker.C:
			ed.triggerFlush(&outbox, flushCh, workersGroup)
		case <-usersResetTicker.C:
			userKeys.clear()
		}
	}
}

func (ed *eventDispatcher) processEvent(evt Event, outbox *eventBuffer, userKeys *lruCache) {

	// Always record the event in the summarizer.
	outbox.addToSummary(evt)

	// Decide whether to add the event to the payload. Feature events may be added twice, once for
	// the event (if tracked) and once for debugging.
	willAddFullEvent := false
	var debugEvent Event
	switch evt := evt.(type) {
	case FeatureRequestEvent:
		if ed.shouldSampleEvent() {
			willAddFullEvent = evt.TrackEvents
			if ed.shouldDebugEvent(&evt) {
				de := evt
				de.Debug = true
				debugEvent = de
			}
		}
	default:
		willAddFullEvent = ed.shouldSampleEvent()
	}

	// For each user we haven't seen before, we add an index event - unless this is already
	// an identify event for that user. This should be added before the event that referenced
	// the user, and can be omitted if that event will contain an inline user.
	if !(willAddFullEvent && ed.config.InlineUsersInEvents) {
		user := evt.GetBase().User
		if !noticeUser(userKeys, &user) {
			if _, ok := evt.(IdentifyEvent); !ok {
				indexEvent := IndexEvent{
					BaseEvent{CreationDate: evt.GetBase().CreationDate, User: user},
				}
				outbox.addEvent(indexEvent)
			}
		}
	}
	if willAddFullEvent {
		outbox.addEvent(evt)
	}
	if debugEvent != nil {
		outbox.addEvent(debugEvent)
	}
}

// Add to the set of users we've noticed, and return true if the user was already known to us.
func noticeUser(userKeys *lruCache, user *User) bool {
	if user == nil || user.Key == nil {
		return true
	}
	return userKeys.add(*user.Key)
}

func (ed *eventDispatcher) shouldSampleEvent() bool {
	return ed.config.SamplingInterval == 0 || rand.Int31n(ed.config.SamplingInterval) == 0
}

func (ed *eventDispatcher) shouldDebugEvent(evt *FeatureRequestEvent) bool {
	if evt.DebugEventsUntilDate == nil {
		return false
	}
	// The "last known past time" comes from the last HTTP response we got from the server.
	// In case the client's time is set wrong, at least we know that any expiration date
	// earlier than that point is definitely in the past.  If there's any discrepancy, we
	// want to err on the side of cutting off event debugging sooner.
	ed.stateLock.Lock() // This should be done infrequently since it's only for debug events
	defer ed.stateLock.Unlock()
	return *evt.DebugEventsUntilDate > ed.lastKnownPastTime &&
		*evt.DebugEventsUntilDate > now()
}

// Signal that we would like to do a flush as soon as possible.
func (ed *eventDispatcher) triggerFlush(outbox *eventBuffer, flushCh chan<- *flushPayload,
	workersGroup *sync.WaitGroup) {
	if ed.isDisabled() {
		outbox.clear()
		return
	}
	// Is there anything to flush?
	payload := outbox.getPayload()
	if len(payload.events) == 0 && len(payload.summary.counters) == 0 {
		return
	}
	workersGroup.Add(1) // Increment the count of active flushes
	select {
	case flushCh <- &payload:
		// If the channel wasn't full, then there is a worker available who will pick up
		// this flush payload and send it. The event outbox and summary state can now be
		// cleared from the main goroutine.
		outbox.clear()
	default:
		// We can't start a flush right now because we're waiting for one of the workers
		// to pick up the last one.  Do not reset the event outbox or summary state.
		workersGroup.Done()
	}
}

func (ed *eventDispatcher) isDisabled() bool {
	// Since we're using a mutex, we should avoid calling this often.
	ed.stateLock.Lock()
	defer ed.stateLock.Unlock()
	return ed.disabled
}

func (ed *eventDispatcher) handleResponse(resp *http.Response) {
	if err := checkForHttpError(resp.StatusCode, resp.Request.URL.String()); err != nil {
		ed.config.Loggers.Error(httpErrorMessage(resp.StatusCode, "posting events", "some events were dropped"))
		if !isHTTPErrorRecoverable(resp.StatusCode) {
			ed.stateLock.Lock()
			defer ed.stateLock.Unlock()
			ed.disabled = true
		}
	} else {
		dt, err := http.ParseTime(resp.Header.Get("Date"))
		if err == nil {
			ed.stateLock.Lock()
			defer ed.stateLock.Unlock()
			ed.lastKnownPastTime = toUnixMillis(dt)
		}
	}
}

func (b *eventBuffer) addEvent(event Event) {
	if len(b.events) >= b.capacity {
		if !b.capacityExceeded {
			b.capacityExceeded = true
			b.loggers.Warn("Exceeded event queue capacity. Increase capacity to avoid dropping events.")
		}
		return
	}
	b.capacityExceeded = false
	b.events = append(b.events, event)
}

func (b *eventBuffer) addToSummary(event Event) {
	b.summarizer.summarizeEvent(event)
}

func (b *eventBuffer) getPayload() flushPayload {
	return flushPayload{
		events:  b.events,
		summary: b.summarizer.snapshot(),
	}
}

func (b *eventBuffer) clear() {
	b.events = make([]Event, 0, b.capacity)
	b.summarizer.reset()
}

func startFlushTask(sdkKey string, config Config, client *http.Client, flushCh <-chan *flushPayload,
	workersGroup *sync.WaitGroup, responseFn func(*http.Response)) {
	ef := eventOutputFormatter{
		userFilter:  newUserFilter(config),
		inlineUsers: config.InlineUsersInEvents,
		config:      config,
	}
	uri := config.EventsEndpointUri
	if uri == "" {
		uri = strings.TrimRight(config.EventsUri, "/") + defaultURIPath
	}
	t := sendEventsTask{
		client:    client,
		eventsURI: uri,
		loggers:   config.Loggers,
		sdkKey:    sdkKey,
		userAgent: config.UserAgent,
		formatter: ef,
	}
	go t.run(flushCh, responseFn, workersGroup)
}

func (t *sendEventsTask) run(flushCh <-chan *flushPayload, responseFn func(*http.Response),
	workersGroup *sync.WaitGroup) {
	for {
		payload, more := <-flushCh
		if !more {
			// Channel has been closed - we're shutting down
			break
		}
		outputEvents := t.formatter.makeOutputEvents(payload.events, payload.summary)
		if len(outputEvents) > 0 {
			resp := t.postEvents(outputEvents)
			if resp != nil {
				responseFn(resp)
			}
		}
		workersGroup.Done() // Decrement the count of in-progress flushes
	}
}

func (t *sendEventsTask) postEvents(outputEvents []interface{}) *http.Response {
	jsonPayload, marshalErr := json.Marshal(outputEvents)
	if marshalErr != nil {
		t.loggers.Errorf("Unexpected error marshalling event json: %+v", marshalErr)
		return nil
	}

	t.loggers.Debugf("Sending %d events: %s", len(outputEvents), jsonPayload)

	var resp *http.Response
	var respErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			t.loggers.Warn("Will retry posting events after 1 second")
			time.Sleep(1 * time.Second)
		}
		req, reqErr := http.NewRequest("POST", t.eventsURI, bytes.NewReader(jsonPayload))
		if reqErr != nil {
			t.loggers.Errorf("Unexpected error while creating event request: %+v", reqErr)
			return nil
		}

		req.Header.Add("Authorization", t.sdkKey)
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("User-Agent", t.userAgent)
		req.Header.Add(eventSchemaHeader, currentEventSchema)

		resp, respErr = t.client.Do(req)

		if resp != nil && resp.Body != nil {
			_, _ = ioutil.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}

		if respErr != nil {
			t.loggers.Warnf("Unexpected error while sending events: %+v", respErr)
			continue
		} else if resp.StatusCode >= 400 && isHTTPErrorRecoverable(resp.StatusCode) {
			t.loggers.Warnf("Received error status %d when sending events", resp.StatusCode)
			continue
		} else {
			break
		}
	}
	return resp
}
