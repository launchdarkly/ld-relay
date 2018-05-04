package ldclient

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// EventProcessor defines the interface for dispatching analytics events.
type EventProcessor interface {
	// Records an event asynchronously.
	SendEvent(Event)
	// Specifies that any buffered events should be sent as soon as possible, rather than waiting
	// for the next flush interval. This method is asynchronous, so events still may not be sent
	// until a later time.
	Flush()
	// Shuts down all event processor activity, after first ensuring that all events have been
	// delivered. Subsequent calls to SendEvent() or Flush() will be ignored.
	Close() error
}

type nullEventProcessor struct{}

type defaultEventProcessor struct {
	inputCh   chan eventDispatcherMessage
	closeOnce sync.Once
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
	logger           Logger
}

type flushPayload struct {
	events  []Event
	summary eventSummary
}

type sendEventsTask struct {
	client    *http.Client
	eventsURI string
	logger    Logger
	sdkKey    string
	userAgent string
	formatter eventOutputFormatter
}

// Payload of the inputCh channel.
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
func NewDefaultEventProcessor(sdkKey string, config Config, client *http.Client) *defaultEventProcessor {
	if client == nil {
		client = &http.Client{}
	}
	inputCh := make(chan eventDispatcherMessage, config.Capacity)
	startEventDispatcher(sdkKey, config, client, inputCh)
	return &defaultEventProcessor{
		inputCh: inputCh,
	}
}

func (ep *defaultEventProcessor) SendEvent(e Event) {
	ep.inputCh <- sendEventMessage{event: e}
}

func (ep *defaultEventProcessor) Flush() {
	ep.inputCh <- flushEventsMessage{}
}

func (ep *defaultEventProcessor) Close() error {
	ep.closeOnce.Do(func() {
		ep.inputCh <- flushEventsMessage{}
		m := shutdownEventsMessage{replyCh: make(chan struct{})}
		ep.inputCh <- m
		<-m.replyCh
	})
	return nil
}

// used only for testing - ensures that all pending messages and flushes have completed
func (ep *defaultEventProcessor) waitUntilInactive() {
	m := syncEventsMessage{replyCh: make(chan struct{})}
	ep.inputCh <- m
	<-m.replyCh // Now we know that all events prior to this call have been processed
}

func startEventDispatcher(sdkKey string, config Config, client *http.Client,
	inputCh <-chan eventDispatcherMessage) {
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
	go ed.runMainLoop(inputCh, flushCh, &workersGroup)
}

func (ed *eventDispatcher) runMainLoop(inputCh <-chan eventDispatcherMessage,
	flushCh chan<- *flushPayload, workersGroup *sync.WaitGroup) {
	if err := recover(); err != nil {
		ed.config.Logger.Printf("Unexpected panic in event processing thread: %+v", err)
	}

	buffer := eventBuffer{
		events:     make([]Event, 0, ed.config.Capacity),
		summarizer: newEventSummarizer(),
		capacity:   ed.config.Capacity,
		logger:     ed.config.Logger,
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
		case message := <-inputCh:
			switch m := message.(type) {
			case sendEventMessage:
				ed.processEvent(m.event, &buffer, &userKeys)
			case flushEventsMessage:
				ed.triggerFlush(&buffer, flushCh, workersGroup)
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
			ed.triggerFlush(&buffer, flushCh, workersGroup)
		case <-usersResetTicker.C:
			userKeys.clear()
		}
	}
}

func (ed *eventDispatcher) processEvent(evt Event, buffer *eventBuffer, userKeys *lruCache) {

	// Always record the event in the summarizer.
	buffer.addToSummary(evt)

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
				buffer.addEvent(indexEvent)
			}
		}
	}
	if willAddFullEvent {
		buffer.addEvent(evt)
	}
	if debugEvent != nil {
		buffer.addEvent(debugEvent)
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
func (ed *eventDispatcher) triggerFlush(buffer *eventBuffer, flushCh chan<- *flushPayload,
	workersGroup *sync.WaitGroup) {
	if ed.isDisabled() {
		buffer.clear()
		return
	}
	// Is there anything to flush?
	payload := buffer.getPayload()
	if len(payload.events) == 0 && len(payload.summary.counters) == 0 {
		return
	}
	workersGroup.Add(1) // Increment the count of active flushes
	select {
	case flushCh <- &payload:
		// If the channel wasn't full, then there is a worker available who will pick up
		// this flush payload and send it. The event buffer and summary state can now be
		// cleared from the main goroutine.
		buffer.clear()
	default:
		// We can't start a flush right now because we're waiting for one of the workers
		// to pick up the last one.  Do not reset the event buffer or summary state.
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
	err := checkStatusCode(resp.StatusCode, resp.Request.URL.String())
	if err != nil {
		ed.config.Logger.Printf("Unexpected status code when sending events: %+v", err)
		if err != nil && err.Code == 401 {
			ed.config.Logger.Printf("Received 401 error, no further events will be posted since SDK key is invalid")
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
			b.logger.Printf("WARN: Exceeded event queue capacity. Increase capacity to avoid dropping events.")
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
	}
	t := sendEventsTask{
		client:    client,
		eventsURI: config.EventsUri + "/bulk",
		logger:    config.Logger,
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
		t.logger.Printf("Unexpected error marshalling event json: %+v", marshalErr)
		return nil
	}

	var resp *http.Response
	var respErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			t.logger.Printf("Will retry posting events after 1 second")
			time.Sleep(1 * time.Second)
		}
		req, reqErr := http.NewRequest("POST", t.eventsURI, bytes.NewReader(jsonPayload))
		if reqErr != nil {
			t.logger.Printf("Unexpected error while creating event request: %+v", reqErr)
			return nil
		}

		req.Header.Add("Authorization", t.sdkKey)
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("User-Agent", t.userAgent)
		req.Header.Add(eventSchemaHeader, currentEventSchema)

		resp, respErr = t.client.Do(req)

		if resp != nil && resp.Body != nil {
			ioutil.ReadAll(resp.Body)
			resp.Body.Close()
		}

		if respErr != nil {
			t.logger.Printf("Unexpected error while sending events: %+v", respErr)
			continue
		} else if resp.StatusCode >= 500 {
			t.logger.Printf("Received error status %d when sending events", resp.StatusCode)
			continue
		} else {
			break
		}
	}
	return resp
}
