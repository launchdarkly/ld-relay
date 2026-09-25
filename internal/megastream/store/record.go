package store

import (
	"fmt"
	"sync"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"

	"github.com/launchdarkly/ld-relay/v9/internal/megastream/wire"
)

// Object kinds this relay can parse. The set of kinds on the wire is open: an object of any
// other kind is stored, served on byte-through surfaces, and evicted normally, but the
// surfaces where relay evaluates serve only the kinds they understand.
const (
	KindFlag    = "flag"
	KindSegment = "segment"
)

// ObjectID is an object's composite identity. Keys are unique only within a kind, and kind
// is an opaque discriminator everywhere in the protocol.
type ObjectID struct {
	Kind string
	Key  string
}

func (id ObjectID) String() string {
	return id.Kind + "/" + id.Key
}

// Record is one object in the base store.
//
// A record is immutable: an update replaces the pointer rather than editing in place, so a
// reader holding one from a snapshot is never invalidated, and the lazy parse needs no lock
// beyond its own. That immutability is the reason every field is unexported -- an exported
// one is a shared mutable field, and one credential's serving path writing through it would
// change what every other credential is served.
type Record struct {
	version wire.PayloadVersion
	raw     []byte

	kind       string
	parseOnce  sync.Once
	parsedItem any
	parseError error
}

// Version is the base payload version at which this object's last change applied. It is not
// the object's own version, which lives inside the content.
func (r *Record) Version() wire.PayloadVersion {
	return r.version
}

// Bytes returns the object's JSON exactly as it arrived, for the surfaces that splice it
// without re-encoding.
//
// The slice is shared with every other reader of this object. Treat it as read-only: it must
// not be written through or appended to, because the same backing array is what every
// credential that sees this object is served, and a write would also race every concurrent
// reader.
func (r *Record) Bytes() []byte {
	return r.raw
}

// Item returns the object parsed into its model form, parsing on first need and memoizing
// the result for this record. Because a record is per version, one hot object's reparse does
// not reparse the world.
//
// It returns a nil item for a kind this relay does not recognize. Valid JSON the model
// cannot parse comes back as an error: that is most likely a schema newer than this relay's,
// so the caller should treat the object as absent locally while byte-through surfaces keep
// forwarding it.
//
// The returned value is shared with every other reader of this object, and evaluation is
// the one consumer that needs it. Treat it as read-only for the same reason as Bytes.
func (r *Record) Item() (any, error) {
	r.parseOnce.Do(func() {
		if r.raw == nil {
			return
		}
		switch r.kind {
		case KindFlag:
			var flag ldmodel.FeatureFlag
			if err := flag.UnmarshalJSON(r.raw); err != nil {
				r.parseError = fmt.Errorf("megastream: cannot parse flag: %w", err)
				return
			}
			r.parsedItem = &flag
		case KindSegment:
			var segment ldmodel.Segment
			if err := segment.UnmarshalJSON(r.raw); err != nil {
				r.parseError = fmt.Errorf("megastream: cannot parse segment: %w", err)
				return
			}
			r.parsedItem = &segment
		}
	})
	return r.parsedItem, r.parseError
}

// Present reports whether the store holds content for this object. A nil record, which is
// what a map lookup yields for an object the store does not hold, is not present.
func (r *Record) Present() bool {
	return r != nil && r.raw != nil
}
