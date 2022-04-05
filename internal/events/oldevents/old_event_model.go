package oldevents

import (
	"errors"

	"github.com/launchdarkly/go-jsonstream/v2/jreader"
	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	"github.com/launchdarkly/go-sdk-common/v3/ldreason"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	ldevents "github.com/launchdarkly/go-sdk-events/v2"
)

var errEventHadNoUserOrContext = errors.New("received an event with no user or context property")

const (
	featureKind  = "feature"
	identifyKind = "identify"
	customKind   = "custom"
)

// ReceivedEventContext wraps the ldevents.EventContext type to add appropriate JSON unmarshaling behavior.
// When we are processing old event data, we do not want to transform the user/context properties at all,
// because an SDK has already done so-- e.g. if private attributes had to be removed, they have already been
// removed. So, we use the special "preserialized" mode that tells ldevents to treat this as a raw blob.
type ReceivedEventContext struct {
	ldevents.EventContext
}

// UnmarshalJSON captures the original JSON user data and treats it as a context.
func (r *ReceivedEventContext) UnmarshalJSON(data []byte) error {
	var minimalContext ldcontext.Context
	reader := jreader.NewReader(data)
	if err := ldcontext.ContextSerialization.UnmarshalWithKindAndKeyOnly(&reader, &minimalContext); err != nil {
		return err
	}
	r.EventContext = ldevents.PreserializedContext(minimalContext, data)
	return nil
}

// OldEvent is an interface implemented by all event types parsed by this package.
type OldEvent interface {
	Kind() string
}

// FeatureEvent represents a "feature" (evaluation) event in old event data.
//
// See package comments in package_info.go for more details on how this differs from the new model.
type FeatureEvent struct {
	actualContext        ldevents.EventContext
	CreationDate         ldtime.UnixMillisecondTime `json:"creationDate"`
	Key                  string                     `json:"key"`
	User                 *ReceivedEventContext      `json:"user"`
	Context              *ReceivedEventContext      `json:"context"`
	Version              ldvalue.OptionalInt        `json:"version"`
	Variation            ldvalue.OptionalInt        `json:"variation"`
	Value                ldvalue.Value              `json:"value"`
	Default              ldvalue.Value              `json:"default"`
	TrackEvents          ldvalue.OptionalBool       `json:"trackEvents"`
	DebugEventsUntilDate ldtime.UnixMillisecondTime `json:"debugEventsUntilDate"`
	Reason               ldreason.EvaluationReason  `json:"reason"`
}

func (e FeatureEvent) Kind() string { return featureKind } //nolint:golint

// IdentifyEvent represents an "identify" event in old event data.
//
// See package comments in package_info.go for more details on how this differs from the new model.
type IdentifyEvent struct {
	actualContext ldevents.EventContext
	CreationDate  ldtime.UnixMillisecondTime `json:"creationDate"`
	User          *ReceivedEventContext      `json:"user"`
	Context       *ReceivedEventContext      `json:"context"`
}

func (e IdentifyEvent) Kind() string { return identifyKind } //nolint:golint

// CustomEvent represents a "custom" event in old event data.
//
// See package comments in package_info.go for more details on how this differs from the new model.
type CustomEvent struct {
	actualContext ldevents.EventContext
	CreationDate  ldtime.UnixMillisecondTime `json:"creationDate"`
	Key           string                     `json:"key"`
	User          *ReceivedEventContext      `json:"user"`
	Context       *ReceivedEventContext      `json:"context"`
	Data          ldvalue.Value              `json:"data"`
	MetricValue   *float64                   `json:"metricValue"`
}

func (e CustomEvent) Kind() string { return customKind } //nolint:golint

// UntranslatedEvent represents an event that we do not implement any special processing for.
type UntranslatedEvent struct {
	RawEvent []byte
	kind     string
}

func (e UntranslatedEvent) Kind() string { return e.kind } //nolint:golint
