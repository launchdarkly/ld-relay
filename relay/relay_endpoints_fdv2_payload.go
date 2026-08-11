package relay

import (
	"github.com/launchdarkly/go-jsonstream/v3/jwriter"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
)

// fdv2PayloadWriter builds the FDv2 polling response document, {"events":[...]}, in a single
// jwriter pass. It produces the same JSON as marshaling a struct of subsystems event types
// with encoding/json, but each event -- including the object bodies of put-object events --
// is written directly into one output buffer. That avoids the per-item buffers, interface
// boxing, and encoding/json's re-scan of embedded raw JSON that marshaling the equivalent
// structs costs. The internal/streams package makes the same guarantee for the SSE
// representation of these events.
type fdv2PayloadWriter struct {
	writer     jwriter.Writer
	topObj     jwriter.ObjectState
	events     jwriter.ArrayState
	eventCount int

	// putEventObj and putDataObj hold the enclosing states of a put-object event between
	// beginPutObject and endPutObject, while the caller writes the object body.
	putEventObj jwriter.ObjectState
	putDataObj  jwriter.ObjectState
}

func newFDv2PayloadWriter() *fdv2PayloadWriter {
	p := &fdv2PayloadWriter{writer: jwriter.NewWriter()}
	p.topObj = p.writer.Object()
	p.events = p.topObj.Name("events").Array()
	return p
}

// writeServerIntent appends a server-intent event. The intent has a single payload, but the
// protocol allows several, so it is written as a one-element array.
func (p *fdv2PayloadWriter) writeServerIntent(payload subsystems.Payload) {
	eventObj := p.events.Object()
	eventObj.Name("event").String(string(subsystems.EventServerIntent))
	dataObj := eventObj.Name("data").Object()
	payloads := dataObj.Name("payloads").Array()
	payloadObj := payloads.Object()
	payloadObj.Name("id").String(payload.ID)
	payloadObj.Name("target").Int(payload.Target)
	payloadObj.Name("intentCode").String(string(payload.Code))
	payloadObj.Name("reason").String(payload.Reason)
	payloadObj.End()
	payloads.End()
	dataObj.End()
	eventObj.End()
	p.eventCount++
}

// beginPutObject appends a put-object event up to its "object" property and returns the
// Writer positioned at that value. The caller writes exactly one JSON value to it and then
// calls endPutObject.
func (p *fdv2PayloadWriter) beginPutObject(version int, kind subsystems.ObjectKind, key string) *jwriter.Writer {
	p.putEventObj = p.events.Object()
	p.putEventObj.Name("event").String(string(subsystems.EventPutObject))
	p.putDataObj = p.putEventObj.Name("data").Object()
	p.putDataObj.Name("version").Int(version)
	p.putDataObj.Name("kind").String(string(kind))
	p.putDataObj.Name("key").String(key)
	return p.putDataObj.Name("object")
}

func (p *fdv2PayloadWriter) endPutObject() {
	p.putDataObj.End()
	p.putEventObj.End()
	p.eventCount++
}

// writePayloadTransferred appends a payload-transferred event carrying the selector.
func (p *fdv2PayloadWriter) writePayloadTransferred(selector subsystems.Selector) {
	eventObj := p.events.Object()
	eventObj.Name("event").String(string(subsystems.EventPayloadTransferred))
	dataObj := eventObj.Name("data").Object()
	dataObj.Name("state").String(selector.State())
	dataObj.Name("version").Int(selector.Version())
	dataObj.End()
	eventObj.End()
	p.eventCount++
}

// finish closes the document and returns the encoded payload and the number of events it
// contains.
func (p *fdv2PayloadWriter) finish() ([]byte, int, error) {
	p.events.End()
	p.topObj.End()
	if err := p.writer.Error(); err != nil {
		return nil, 0, err
	}
	return p.writer.Bytes(), p.eventCount, nil
}
