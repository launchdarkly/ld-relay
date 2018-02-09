package ldclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/ioutil"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

type eventProcessor struct {
	queue  []Event
	sdkKey string
	config Config
	closed bool
	mu     *sync.Mutex
	client *http.Client
	closer chan struct{}
}

type Event interface {
	GetBase() BaseEvent
	GetKind() string
}

type BaseEvent struct {
	CreationDate uint64 `json:"creationDate"`
	Key          string `json:"key"`
	Kind         string `json:"kind"`
	User         User   `json:"user"`
}

type FeatureRequestEvent struct {
	BaseEvent
	Value    interface{} `json:"value"`
	Default  interface{} `json:"default"`
	Version  *int        `json:"version,omitempty"`
	PrereqOf *string     `json:"prereqOf,omitempty"`
}

const (
	FEATURE_REQUEST_EVENT = "feature"
	CUSTOM_EVENT          = "custom"
	IDENTIFY_EVENT        = "identify"
)

func newEventProcessor(sdkKey string, config Config) *eventProcessor {
	res := &eventProcessor{
		queue:  make([]Event, 0),
		sdkKey: sdkKey,
		config: config,
		client: &http.Client{},
		closer: make(chan struct{}),
		mu:     &sync.Mutex{},
	}

	go func() {
		if err := recover(); err != nil {
			res.config.Logger.Printf("Unexpected panic in event processing thread: %+v", err)
		}

		ticker := time.NewTicker(config.FlushInterval)
		for {
			select {
			case <-ticker.C:
				res.flush()
			case <-res.closer:
				ticker.Stop()
				return
			}
		}
	}()

	return res
}

func (ep *eventProcessor) close() {
	ep.mu.Lock()
	closed := ep.closed
	ep.closed = true
	ep.mu.Unlock()

	if !closed {
		close(ep.closer)
		ep.flush()
	}
}

func (ep *eventProcessor) flush() {
	uri := ep.config.EventsUri + "/bulk"
	ep.mu.Lock()

	if len(ep.queue) == 0 || ep.closed {
		ep.mu.Unlock()
		return
	}

	events := ep.queue
	ep.queue = make([]Event, 0)
	ep.mu.Unlock()

	payload, marshalErr := json.Marshal(events)

	if marshalErr != nil {
		ep.config.Logger.Printf("Unexpected error marshalling event json: %+v", marshalErr)
	}

	req, reqErr := http.NewRequest("POST", uri, bytes.NewReader(payload))

	if reqErr != nil {
		ep.config.Logger.Printf("Unexpected error while creating event request: %+v", reqErr)
	}

	req.Header.Add("Authorization", ep.sdkKey)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("User-Agent", "GoClient/"+Version)

	resp, respErr := ep.client.Do(req)

	defer func() {
		if resp != nil && resp.Body != nil {
			ioutil.ReadAll(resp.Body)
			resp.Body.Close()
		}
	}()

	if respErr != nil {
		ep.config.Logger.Printf("Unexpected error while sending events: %+v", respErr)
		return
	}
	err := checkStatusCode(resp.StatusCode, uri)
	if err != nil {
		ep.config.Logger.Printf("Unexpected status code when sending events: %+v", err)
		if err != nil && err.Code == 401 {
			ep.config.Logger.Printf("Received 401 error, no further events will be posted since SDK key is invalid")
			ep.mu.Lock()
			ep.closed = true
			ep.mu.Unlock()
		}
	}
}

func (ep *eventProcessor) sendEvent(evt Event) error {
	if !ep.config.SendEvents {
		return nil
	}

	if ep.config.SamplingInterval > 0 && rand.Int31n(ep.config.SamplingInterval) != 0 {
		return nil
	}

	scrubbedUser := scrubUser(evt.GetBase().User, ep.config.AllAttributesPrivate, ep.config.PrivateAttributeNames) 	
	var newEvent Event
	switch evt := evt.(type) {
	case FeatureRequestEvent:
		evt.User = scrubbedUser
		newEvent = evt
	case CustomEvent:
		evt.User = scrubbedUser
		newEvent = evt
	case IdentifyEvent:
		evt.User = scrubbedUser
		newEvent = evt
	default:
		return errors.New("unknown event type")
	}

	ep.mu.Lock()
	defer ep.mu.Unlock()

	if ep.closed {
		return nil
	}
	if len(ep.queue) >= ep.config.Capacity {
 		return errors.New("Exceeded event queue capacity. Increase capacity to avoid dropping events.")
 	}
	ep.queue = append(ep.queue, newEvent)
	return nil
}

// Used to just create the event. Normally, you don't need to call this;
// the event is created and queued automatically during feature flag evaluation.
func NewFeatureRequestEvent(key string, user User, value, defaultVal interface{}, version *int, prereqOf *string) FeatureRequestEvent {
	return FeatureRequestEvent{
		BaseEvent: BaseEvent{
			CreationDate: now(),
			Key:          key,
			User:         user,
			Kind:         FEATURE_REQUEST_EVENT,
		},
		Value:    value,
		Default:  defaultVal,
		Version:  version,
		PrereqOf: prereqOf,
	}
}

func (evt FeatureRequestEvent) GetBase() BaseEvent {
	return evt.BaseEvent
}

func (evt FeatureRequestEvent) GetKind() string {
	return evt.Kind
}

type CustomEvent struct {
	BaseEvent
	Data interface{} `json:"data"`
}

// Constructs a new custom event, but does not send it. Typically, Track should be used to both create the
// event and send it to LaunchDarkly.
func NewCustomEvent(key string, user User, data interface{}) CustomEvent {
	return CustomEvent{
		BaseEvent: BaseEvent{
			CreationDate: now(),
			Key:          key,
			User:         user,
			Kind:         CUSTOM_EVENT,
		},
		Data: data,
	}
}

func (evt CustomEvent) GetBase() BaseEvent {
	return evt.BaseEvent
}

func (evt CustomEvent) GetKind() string {
	return evt.Kind
}

type IdentifyEvent struct {
	BaseEvent
}

// Constructs a new identify event, but does not send it. Typically, Identify should be used to both create the
// event and send it to LaunchDarkly.
func NewIdentifyEvent(user User) IdentifyEvent {
	var key string
	if user.Key == nil {
		key = ""
	} else {
		key = *user.Key
	}
	return IdentifyEvent{
		BaseEvent: BaseEvent{
			CreationDate: now(),
			Key:          key,
			User:         user,
			Kind:         IDENTIFY_EVENT,
		},
	}
}

func (evt IdentifyEvent) GetBase() BaseEvent {
	return evt.BaseEvent
}

func (evt IdentifyEvent) GetKind() string {
	return evt.Kind
}

func now() uint64 {
	return toUnixMillis(time.Now())
}

func toUnixMillis(t time.Time) uint64 {
	ms := time.Duration(t.UnixNano()) / time.Millisecond

	return uint64(ms)
}

func scrubUser(user User, allAttributesPrivate bool, globalPrivateAttributes []string) User {
	user.PrivateAttributes = nil

	if len(user.PrivateAttributeNames) == 0 && len(globalPrivateAttributes) == 0 && !allAttributesPrivate {
		return user
	}

	isPrivate := map[string]bool{}
	for _, n := range globalPrivateAttributes {
		isPrivate[n] = true
	}
	for _, n := range user.PrivateAttributeNames {
		isPrivate[n] = true
	}

	if user.Custom != nil {
		var custom = map[string]interface{}{}
		for k, v := range *user.Custom {
			if allAttributesPrivate || isPrivate[k] {
				user.PrivateAttributes = append(user.PrivateAttributes, k)
			} else {
				custom[k] = v
			}
		}
		user.Custom = &custom
	}

	if !isEmpty(user.Avatar) && (allAttributesPrivate || isPrivate["avatar"]) {
		user.Avatar = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "avatar")
	}

	if !isEmpty(user.Country) && (allAttributesPrivate || isPrivate["country"]) {
		user.Country = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "country")
	}

	if !isEmpty(user.Ip) && (allAttributesPrivate || isPrivate["ip"]) {
		user.Ip = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "ip")
	}

	if !isEmpty(user.FirstName) && (allAttributesPrivate || isPrivate["firstName"]) {
		user.FirstName = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "firstName")
	}

	if !isEmpty(user.LastName) && (allAttributesPrivate || isPrivate["lastName"]) {
		user.LastName = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "lastName")
	}

	if !isEmpty(user.Name) && (allAttributesPrivate || isPrivate["name"]) {
		user.Name = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "name")
	}

	if !isEmpty(user.Secondary) && (allAttributesPrivate || isPrivate["secondary"]) {
		user.Secondary = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "secondary")
	}

	if !isEmpty(user.Email) && (allAttributesPrivate || isPrivate["email"]) {
		user.Email = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "email")
	}

	return user
}

func isEmpty(s *string) bool {
	return s == nil || *s == ""
}
