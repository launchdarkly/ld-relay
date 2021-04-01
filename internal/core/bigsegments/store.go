package bigsegments

import (
	"io"
	"time"
)

// BigSegmentStore is the interface for interacting with an external big segment store.
type BigSegmentStore interface {
	io.Closer
	// applyPatch is used to apply updates to the store.
	applyPatch(patch bigSegmentPatch) error
	// getCursor loads the synchronization cursor from the external store.
	getCursor(environmentID string) (string, error)
	// setSynchronizedOn stores the synchronization time in the external store
	setSynchronizedOn(environmentID string, synchronizedOn time.Time) error
}
