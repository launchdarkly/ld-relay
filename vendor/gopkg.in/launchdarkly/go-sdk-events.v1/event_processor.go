package ldevents

import (
	"encoding/json"
	"sync"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
)

type defaultEventProcessor struct {
	inboxCh       chan eventDispatcherMessage
	inboxFullOnce sync.Once
	closeOnce     sync.Once
	loggers       ldlog.Loggers
}

type eventDispatcher struct {
	config             EventsConfiguration
	outbox             *eventsOutbox
	flushCh            chan *flushPayload
	workersGroup       *sync.WaitGroup
	userKeys           lruCache
	lastKnownPastTime  ldtime.UnixMillisecondTime
	deduplicatedUsers  int
	eventsInLastBatch  int
	disabled           bool
	currentTimestampFn func() ldtime.UnixMillisecondTime
	stateLock          sync.Mutex
}

type flushPayload struct {
	diagnosticEvent ldvalue.Value
	events          []Event
	summary         eventSummary
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
	maxFlushWorkers = 5
)

// NewDefaultEventProcessor creates an instance of the default implementation of analytics event processing.
func NewDefaultEventProcessor(config EventsConfiguration) EventProcessor {
	inboxCh := make(chan eventDispatcherMessage, config.Capacity)
	startEventDispatcher(config, inboxCh)
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

func (ep *defaultEventProcessor) postNonBlockingMessageToInbox(e eventDispatcherMessage) {
	select {
	case ep.inboxCh <- e:
		return
	default:
	}
	// If the inbox is full, it means the eventDispatcher is seriously backed up with not-yet-processed events.
	// This is unlikely, but if it happens, it means the application is probably doing a ton of flag evaluations
	// across many goroutines-- so if we wait for a space in the inbox, we risk a very serious slowdown of the
	// app. To avoid that, we'll just drop the event. The log warning about this will only be shown once.
	ep.inboxFullOnce.Do(func() {
		ep.loggers.Warn("Events are being produced faster than they can be processed; some events will be dropped")
	})
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

func startEventDispatcher(
	config EventsConfiguration,
	inboxCh <-chan eventDispatcherMessage,
) {
	ed := &eventDispatcher{
		config:             config,
		outbox:             newEventsOutbox(config.Capacity, config.Loggers),
		flushCh:            make(chan *flushPayload, 1),
		workersGroup:       &sync.WaitGroup{},
		userKeys:           newLruCache(config.UserKeysCapacity),
		currentTimestampFn: config.currentTimeProvider,
	}

	if ed.currentTimestampFn == nil {
		ed.currentTimestampFn = ldtime.UnixMillisNow
	}

	// Start a fixed-size pool of workers that wait on flushTriggerCh. This is the
	// maximum number of flushes we can do concurrently.
	for i := 0; i < maxFlushWorkers; i++ {
		go runFlushTask(config, ed.flushCh, ed.workersGroup, ed.handleResult)
	}
	if config.DiagnosticsManager != nil {
		event := config.DiagnosticsManager.CreateInitEvent()
		ed.sendDiagnosticsEvent(event)
	}
	go ed.runMainLoop(inboxCh)
}

func (ed *eventDispatcher) runMainLoop(
	inboxCh <-chan eventDispatcherMessage,
) {
	if err := recover(); err != nil {
		ed.config.Loggers.Errorf("Unexpected panic in event processing thread: %+v", err)
	}

	flushInterval := ed.config.FlushInterval
	if flushInterval <= 0 {
		flushInterval = DefaultFlushInterval
	}
	userKeysFlushInterval := ed.config.UserKeysFlushInterval
	if userKeysFlushInterval <= 0 {
		userKeysFlushInterval = DefaultUserKeysFlushInterval
	}
	flushTicker := time.NewTicker(flushInterval)
	usersResetTicker := time.NewTicker(userKeysFlushInterval)

	var diagnosticsTicker *time.Ticker
	var diagnosticsTickerCh <-chan time.Time
	diagnosticsManager := ed.config.DiagnosticsManager
	if diagnosticsManager != nil {
		interval := ed.config.DiagnosticRecordingInterval
		if interval > 0 {
			if interval < MinimumDiagnosticRecordingInterval {
				interval = DefaultDiagnosticRecordingInterval
			}
		} else {
			if ed.config.forceDiagnosticRecordingInterval > 0 {
				interval = ed.config.forceDiagnosticRecordingInterval
			} else {
				interval = DefaultDiagnosticRecordingInterval
			}
		}
		diagnosticsTicker = time.NewTicker(interval)
		diagnosticsTickerCh = diagnosticsTicker.C
	}

	for {
		// Drain the response channel with a higher priority than anything else
		// to ensure that the flush workers don't get blocked.
		select {
		case message := <-inboxCh:
			switch m := message.(type) {
			case sendEventMessage:
				ed.processEvent(m.event)
			case flushEventsMessage:
				ed.triggerFlush()
			case syncEventsMessage:
				ed.workersGroup.Wait()
				m.replyCh <- struct{}{}
			case shutdownEventsMessage:
				flushTicker.Stop()
				usersResetTicker.Stop()
				if diagnosticsTicker != nil {
					diagnosticsTicker.Stop()
				}
				ed.workersGroup.Wait() // Wait for all in-progress flushes to complete
				close(ed.flushCh)      // Causes all idle flush workers to terminate
				m.replyCh <- struct{}{}
				return
			}
		case <-flushTicker.C:
			ed.triggerFlush()
		case <-usersResetTicker.C:
			ed.userKeys.clear()
		case <-diagnosticsTickerCh:
			if diagnosticsManager == nil || !diagnosticsManager.CanSendStatsEvent() {
				break
			}
			event := diagnosticsManager.CreateStatsEventAndReset(
				ed.outbox.droppedEvents,
				ed.deduplicatedUsers,
				ed.eventsInLastBatch,
			)
			ed.outbox.droppedEvents = 0
			ed.deduplicatedUsers = 0
			ed.eventsInLastBatch = 0
			ed.sendDiagnosticsEvent(event)
		}
	}
}

func (ed *eventDispatcher) processEvent(evt Event) {
	// Always record the event in the summarizer.
	ed.outbox.addToSummary(evt)

	// Decide whether to add the event to the payload. Feature events may be added twice, once for
	// the event (if tracked) and once for debugging.
	willAddFullEvent := true
	var debugEvent Event
	inlinedUser := ed.config.InlineUsersInEvents
	switch evt := evt.(type) {
	case FeatureRequestEvent:
		willAddFullEvent = evt.TrackEvents
		if ed.shouldDebugEvent(&evt) {
			de := evt
			de.Debug = true
			debugEvent = de
		}
	case IdentifyEvent:
		inlinedUser = true
	}

	// For each user we haven't seen before, we add an index event before the event that referenced
	// the user - unless the event must contain an inline user (i.e. if InlineUsersInEvents is true,
	// or if it is an identify event).
	user := evt.GetBase().User
	alreadySeenUser := ed.userKeys.add(user.GetKey())
	if !(willAddFullEvent && inlinedUser) {
		if alreadySeenUser {
			ed.deduplicatedUsers++
		} else {
			indexEvent := IndexEvent{
				BaseEvent{CreationDate: evt.GetBase().CreationDate, User: user},
			}
			ed.outbox.addEvent(indexEvent)
		}
	}
	if willAddFullEvent {
		ed.outbox.addEvent(evt)
	}
	if debugEvent != nil {
		ed.outbox.addEvent(debugEvent)
	}
}

func (ed *eventDispatcher) shouldDebugEvent(evt *FeatureRequestEvent) bool {
	if evt.DebugEventsUntilDate == 0 {
		return false
	}
	// The "last known past time" comes from the last HTTP response we got from the server.
	// In case the client's time is set wrong, at least we know that any expiration date
	// earlier than that point is definitely in the past.  If there's any discrepancy, we
	// want to err on the side of cutting off event debugging sooner.
	ed.stateLock.Lock() // This should be done infrequently since it's only for debug events
	defer ed.stateLock.Unlock()
	return evt.DebugEventsUntilDate > ed.lastKnownPastTime &&
		evt.DebugEventsUntilDate > ed.currentTimestampFn()
}

// Signal that we would like to do a flush as soon as possible.
func (ed *eventDispatcher) triggerFlush() {
	if ed.isDisabled() {
		ed.outbox.clear()
		return
	}
	// Is there anything to flush?
	payload := ed.outbox.getPayload()
	totalEventCount := len(payload.events)
	if len(payload.summary.counters) > 0 {
		totalEventCount++
	}
	if totalEventCount == 0 {
		ed.eventsInLastBatch = 0
		return
	}
	ed.workersGroup.Add(1) // Increment the count of active flushes
	select {
	case ed.flushCh <- &payload:
		// If the channel wasn't full, then there is a worker available who will pick up
		// this flush payload and send it. The event outbox and summary state can now be
		// cleared from the main goroutine.
		ed.eventsInLastBatch = totalEventCount
		ed.outbox.clear()
	default:
		// We can't start a flush right now because we're waiting for one of the workers
		// to pick up the last one.  Do not reset the event outbox or summary state.
		ed.workersGroup.Done()
	}
}

func (ed *eventDispatcher) isDisabled() bool {
	// Since we're using a mutex, we should avoid calling this often.
	ed.stateLock.Lock()
	defer ed.stateLock.Unlock()
	return ed.disabled
}

func (ed *eventDispatcher) handleResult(result EventSenderResult) {
	if result.MustShutDown {
		ed.stateLock.Lock()
		defer ed.stateLock.Unlock()
		ed.disabled = true
	} else if result.TimeFromServer > 0 {
		ed.stateLock.Lock()
		defer ed.stateLock.Unlock()
		ed.lastKnownPastTime = result.TimeFromServer
	}
}

func (ed *eventDispatcher) sendDiagnosticsEvent(
	event ldvalue.Value,
) {
	payload := flushPayload{diagnosticEvent: event}
	ed.workersGroup.Add(1) // Increment the count of active flushes
	select {
	case ed.flushCh <- &payload:
		// If the channel wasn't full, then there is a worker available who will pick up
		// this flush payload and send it.
	default:
		// We can't start a flush right now because we're waiting for one of the workers
		// to pick up the last one. We'll just discard this diagnostic event - presumably
		// we'll send another one later anyway, and we don't want this kind of nonessential
		// data to cause any kind of back-pressure.
		ed.workersGroup.Done()
	}
}

func runFlushTask(config EventsConfiguration, flushCh <-chan *flushPayload,
	workersGroup *sync.WaitGroup, resultFn func(EventSenderResult)) {
	formatter := eventOutputFormatter{
		userFilter: newUserFilter(config),
		config:     config,
	}
	for {
		payload, more := <-flushCh
		if !more {
			// Channel has been closed - we're shutting down
			break
		}
		if !payload.diagnosticEvent.IsNull() {
			bytes, err := json.Marshal(payload.diagnosticEvent)
			if err != nil {
				config.Loggers.Errorf("Unexpected error marshalling diagnostic event: %+v", err)
			} else {
				_ = config.EventSender.SendEventData(DiagnosticEventDataKind, bytes, 1)
			}
		} else {
			outputEvents := formatter.makeOutputEvents(payload.events, payload.summary)
			if len(outputEvents) > 0 {
				bytes, err := json.Marshal(outputEvents)
				if err != nil {
					config.Loggers.Errorf("Unexpected error marshalling event JSON: %+v", err)
				} else {
					result := config.EventSender.SendEventData(AnalyticsEventDataKind, bytes, len(outputEvents))
					resultFn(result)
				}
			}
		}
		workersGroup.Done() // Decrement the count of in-progress flushes
	}
}
