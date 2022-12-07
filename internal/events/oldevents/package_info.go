// Package oldevents contains types and logic for processing event data from older SDKs that use
// obsolete event schemas. The "summarizing" logic in the parent package uses this code to transform
// the event data, converting it into parameters that are then fed into the same event processor
// that is used by the Go SDK.
//
// The schema variants we need to handle are:
//
// 1. Version 1 - assumed as the default if the schema version header is absent. This is from an old
// SDK, prior to the implementation of the new event logic that includes summary events, tracking,
// and debugging.
//
// 2. Version 2 - from PHP SDK 3.1.0 and later. This includes a bit more information.
//
// Regardless of the schema version, one constant is that if an event has a "user" property, we take
// the user properties and do not transform them in any way-- because we assume the SDK has already
// done so, based on its own configuration (e.g. if there were private attributes to be removed,
// they have been removed). Newer PHP SDK versions will send "context" rather than "user", so we
// treat the two property names interchangeably in the input data.
//
// Translating "feature" events
//
// The "feature" (evaluation) event is where all the significant differences are. We currently use
// this event type only for the unusual "full event tracking" mode, but very old SDKs produced one
// of these for every evaluation, and the PHP SDK necessarily must do so because (being stateless)
// it's not capable of computing summary events.
//
// In schema version 1, the "variation" (variation index) property is never provided. We need this
// in order to compute summary events, so we must get the flag data and infer the variation index by
// looking at the flag's variation list.
//
// In schema version 2, if and only if the PHP SDK version is at least 3.6.0, the "trackEvents" and
// "debugEventsUntilDate" properties are set by the SDK, copied from the corresponding flag
// properties, because the SDK knows we will need them as inputs to event processing. If they are
// not present (which is an ambiguous state, because it might just mean they are false/null), we
// must get the flag data.
//
// In any schema version, if the "version" property (flag version) is absent then the flag did not
// exist and we don't need to bother getting the flag data.
//
// Translating "identify" events
//
// The old identify event is basically the same as the new one, except it has a redundant "key"
// property and it refers to the context as "user".
//
// Translating "custom" events
//
// The old custom event differs from the new one in that it could have an inline user; in the new
// model, it has only the context keys, and the rest of the context properties are moved to an
// "index" event by the event processor.
//
// Translating "alias" events
//
// The "alias" event type no longer exists in the new schema, but for simplicity's sake, we are
// allowed to send it as part of a payload in the new schema if we have received it from an older
// SDK. We simply pass this event (and any other event whose kind we don't recognize) as an
// unchanged JSON object.
package oldevents
