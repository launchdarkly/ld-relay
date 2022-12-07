package oldevents

import (
	"encoding/json"
	"errors"

	ldevents "github.com/launchdarkly/go-sdk-events/v2"
)

var (
	errBadEvent = errors.New("invalid event data")
)

// UnmarshalEvent attempts to unmarshal a JSON object blob into an event.
func UnmarshalEvent(rawEvent []byte) (OldEvent, error) {
	var kindFieldOnly struct {
		Kind string
	}
	if err := json.Unmarshal(rawEvent, &kindFieldOnly); err != nil {
		return nil, err
	}
	if kindFieldOnly.Kind == "" {
		return nil, errBadEvent
	}

	var err error
	var ret OldEvent
	contextOrUser := func(context *ReceivedEventContext, user *ReceivedEventContext) (ldevents.EventInputContext, error) {
		if context != nil {
			return context.EventInputContext, nil
		}
		if user != nil {
			return user.EventInputContext, nil
		}
		return ldevents.EventInputContext{}, errEventHadNoUserOrContext
	}

	switch kindFieldOnly.Kind {
	case "feature":
		var e FeatureEvent
		err = json.Unmarshal(rawEvent, &e)
		if err == nil {
			e.actualContext, err = contextOrUser(e.Context, e.User)
		}
		ret = e

	case "custom":
		var e CustomEvent
		err = json.Unmarshal(rawEvent, &e)
		if err == nil {
			e.actualContext, err = contextOrUser(e.Context, e.User)
		}
		ret = e

	case "identify":
		var e IdentifyEvent
		err = json.Unmarshal(rawEvent, &e)
		if err == nil {
			e.actualContext, err = contextOrUser(e.Context, e.User)
		}
		ret = e

	default:
		ret = UntranslatedEvent{kind: kindFieldOnly.Kind, RawEvent: rawEvent}
	}

	return ret, err
}
