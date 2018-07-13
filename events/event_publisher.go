package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	ld "gopkg.in/launchdarkly/go-client.v4"

	"gopkg.in/launchdarkly/ld-relay.v5/version"
)

const (
	numAttempts          = 2
	defaultFlushInterval = time.Minute
	defaultCapacity      = 1000
	inputQueueSize       = 100
	defaultEventsURI     = "https://events.launchdarkly.com/bulk"
)

var (
	defaultUserAgent = "LDRelay/" + version.Version
)

type EventPublisher interface {
	Publish(...interface{})
	PublishRaw(...json.RawMessage)
	Flush()
}

type HttpEventPublisher struct {
	eventsURI  string
	logger     ld.Logger
	client     *http.Client
	sdkKey     string
	userAgent  string
	closer     chan<- struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup
	inputQueue chan interface{}
	queue      []interface{}
	capacity   int
}

type batch []interface{}
type rawBatch []json.RawMessage
type flush struct{}

func (b batch) append(q *[]interface{}, max int, logger ld.Logger, reachedCapacity *bool) {
	available := max - len(*q)
	taken := len(b)
	if available < len(b) {
		if !*reachedCapacity {
			logger.Printf("WARNING: Exceeded event queue capacity of %d. Increase capacity to avoid dropping events.", max)
		}
		*reachedCapacity = true
		taken = available
	}
	*q = append(*q, b[:taken]...)
}

func (b rawBatch) append(q *[]interface{}, max int, logger ld.Logger, reachedCapacity *bool) {
	available := max - len(*q)
	taken := len(b)
	if available < len(b) {
		if !*reachedCapacity {
			logger.Printf("WARNING: Exceeded event queue capacity of %d. Increase capacity to avoid dropping events.", max)
		}
		*reachedCapacity = true
		taken = available
	}
	for _, e := range b[:taken] {
		*q = append(*q, e)
	}
}

type OptionType interface {
	apply(*HttpEventPublisher) error
}

type OptionUri string

func (o OptionUri) apply(p *HttpEventPublisher) error {
	p.eventsURI = string(o)
	return nil
}

type OptionFlushInterval time.Duration

func (o OptionFlushInterval) apply(p *HttpEventPublisher) error {
	return nil
}

type OptionClient struct {
	*http.Client
}

func (o OptionClient) apply(p *HttpEventPublisher) error {
	p.client = o.Client
	return nil
}

type OptionUserAgent string

func (o OptionUserAgent) apply(p *HttpEventPublisher) error {
	p.userAgent = string(o)
	return nil
}

type OptionLogger struct {
	ld.Logger
}

func (o OptionLogger) apply(p *HttpEventPublisher) error {
	p.logger = o.Logger
	return nil
}

type OptionCapacity int

func (o OptionCapacity) apply(p *HttpEventPublisher) error {
	p.capacity = int(o)
	return nil
}

func NewHttpEventPublisher(sdkKey string, options ...OptionType) (*HttpEventPublisher, error) {
	closer := make(chan struct{})

	inputQueue := make(chan interface{}, inputQueueSize)
	p := &HttpEventPublisher{
		client:     http.DefaultClient,
		userAgent:  defaultUserAgent,
		eventsURI:  defaultEventsURI,
		sdkKey:     sdkKey,
		closer:     closer,
		capacity:   defaultCapacity,
		inputQueue: inputQueue,
		logger:     log.New(os.Stderr, "HttpEventPublisher: ", log.LstdFlags),
	}

	flushInterval := defaultFlushInterval

	for _, o := range options {
		err := o.apply(p)
		if err != nil {
			return nil, err
		}
		switch o := o.(type) {
		case OptionFlushInterval:
			flushInterval = time.Duration(o)
		}
	}

	p.queue = make([]interface{}, 0, p.capacity)
	p.wg.Add(1)

	ticker := time.NewTicker(flushInterval)

	go func() {
		for {
			if err := recover(); err != nil {
				p.logger.Printf("Unexpected panic in event relay : %+v", err)
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
						e.append(&p.queue, p.capacity, p.logger, &reachedCapacity)
					case batch:
						e.append(&p.queue, p.capacity, p.logger, &reachedCapacity)
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

func (p *HttpEventPublisher) Publish(events ...interface{}) {
	p.inputQueue <- batch(events)
}

func (p *HttpEventPublisher) PublishRaw(events ...json.RawMessage) {
	p.inputQueue <- rawBatch(events)
}

func (p *HttpEventPublisher) Flush() {
	p.inputQueue <- flush{}
}

func (p *HttpEventPublisher) flush() {
	if len(p.queue) == 0 {
		return
	}
	payload, err := json.Marshal(p.queue[0:len(p.queue)])
	p.queue = make([]interface{}, 0, p.capacity)
	if err != nil {
		p.logger.Printf("Unexpected error marshalling event json: %+v", err)
		return
	}
	p.wg.Add(1)
	go func() {
		err := p.postEvents(payload)
		if err != nil {
			p.logger.Println(err.Error())
		}
		p.wg.Done()
	}()
}

func (p *HttpEventPublisher) Close() {
	p.closeOnce.Do(func() {
		close(p.closer)
		p.wg.Wait()
	})
}

func (p *HttpEventPublisher) postEvents(jsonPayload []byte) error {
	var resp *http.Response
	var respErr error
PostAttempts:
	for attempt := 0; attempt < numAttempts; attempt++ {
		if attempt > 0 {
			if respErr != nil {
				p.logger.Printf("Unexpected error while sending events: %+v", respErr)
			}
			p.logger.Printf("Will retry posting events after 1 second")
			time.Sleep(1 * time.Second)
		}
		req, reqErr := http.NewRequest("POST", p.eventsURI+"/bulk", bytes.NewReader(jsonPayload))
		if reqErr != nil {
			return reqErr
		}

		req.Header.Add("Authorization", p.sdkKey)
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("User-Agent", "LDRelay/"+version.Version)
		req.Header.Add(EventSchemaHeader, strconv.Itoa(SummaryEventsSchemaVersion))

		resp, respErr = p.client.Do(req)
		if respErr != nil {
			continue
		}

		if resp != nil && resp.Body != nil {
			_, _ = ioutil.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}

		statusCode := resp.StatusCode

		if statusCode/100 == 2 {
			return nil
		}
		respErr = fmt.Errorf("unexpected response code: %d when accessing URL: %s", statusCode, p.eventsURI)

		switch statusCode {
		case http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusNotFound:
			break PostAttempts
		}
	}
	return respErr
}
