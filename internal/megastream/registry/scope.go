package registry

import (
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/megastream/wire"
	"github.com/launchdarkly/ld-relay/v9/internal/retry"
)

// State is where a credential stands on the current connection.
type State int

const (
	// Unregistered means the credential is wanted but is not on the wire: the relay is not
	// connected, or it has not presented the credential yet, or the session's scope bound
	// leaves no room for it.
	Unregistered State = iota

	// Presented means the relay named the credential in a handshake or a registration, and
	// the server has not answered yet.
	Presented

	// Hydrating means the server opened the scope and its delivery is in flight.
	Hydrating

	// Synced means the scope has received a cursor on this connection.
	Synced

	// Refused means the server could not decide: a verification outage or a resource bound.
	// The credential stays presented but is not held. The membership is kept and only the
	// selector is discarded, so the credential's SDKs keep being served.
	Refused

	// Rejected means the server destroyed the registration: the credential failed
	// verification or was revoked. The membership and the selector are discarded, and the
	// credential is not on the wire until the relay presents it again.
	Rejected
)

func (s State) String() string {
	switch s {
	case Unregistered:
		return "unregistered"
	case Presented:
		return "presented"
	case Hydrating:
		return "hydrating"
	case Synced:
		return "synced"
	case Refused:
		return "refused"
	case Rejected:
		return "rejected"
	default:
		return "unknown"
	}
}

// presented reports whether the credential is presented on the current connection, so the
// server counts it, answers it, or both.
func (s State) presented() bool {
	switch s {
	case Presented, Hydrating, Synced, Refused:
		return true
	default:
		return false
	}
}

// occupiesSlot reports whether the credential counts against the session's scope bound. A
// refused credential is presented but not registered, so it holds no slot.
func (s State) occupiesSlot() bool {
	switch s {
	case Presented, Hydrating, Synced:
		return true
	default:
		return false
	}
}

// Kind is a credential's kind, which decides which downstream endpoints may use it. The
// server is authoritative for it; the prefixes on credential values are informative only.
type Kind int

const (
	// KindServerSide is a server-side SDK key.
	KindServerSide Kind = iota
	// KindMobile is a mobile key.
	KindMobile
	// KindClientSide is an environment ID, used by browser SDKs.
	KindClientSide
)

// Desired is one credential the relay wants served, as configuration sees it.
type Desired struct {
	// Key is the credential value.
	Key string

	// Kind decides the endpoints this credential may reach. Configuration supplies it by
	// which field the value appeared in; the server's verification is authoritative for what
	// the credential may see.
	Kind Kind

	// Expires, when non-zero, is when a deprecated credential stops being wanted. A rotation
	// is simply the new key appearing and the old one gaining an expiry.
	Expires time.Time
}

// scope is the registry's record of one credential.
type scope struct {
	desired Desired
	state   State

	// removed records that configuration no longer wants the credential. The record stays
	// until the credential is off the wire, because the relay must not withdraw a
	// presentation that the server has not answered yet.
	removed bool

	// servable latches the first time the scope receives a cursor and is cleared only by
	// destruction. Nothing transient un-serves a credential: a disconnect, a resync and a
	// refusal all leave the scope serving what it has.
	servable bool

	// retry is the backoff after a rejection or a refusal, or nil when the credential has
	// not failed since its last cursor.
	retry *retry.Strategy

	// retryAt is when a rejected or refused credential may be presented again. A rejection
	// cannot be told apart from "not valid yet", because auto-configuration can outrun the
	// propagation of a freshly created key, so a rejected key is retried too.
	retryAt time.Time

	// lastError is the most recent scope-tagged code, for reporting.
	lastError string
}

// wants reports whether configuration still wants this credential at the given time.
func (s *scope) wants(now time.Time) bool {
	if s.removed {
		return false
	}
	return s.desired.Expires.IsZero() || now.Before(s.desired.Expires)
}

// destroysScope reports whether a scope-tagged error code means the server destroyed the
// registration, so the membership and the selector both go.
func destroysScope(code string) bool {
	switch code {
	case wire.ErrorInvalidKey, wire.ErrorScopeRevoked:
		return true
	default:
		return false
	}
}

// refusesScope reports whether a code means the server could not decide. The membership is
// kept so the credential's SDKs keep being served. The selector goes, because while the
// credential is unregistered the base store may lose objects that only it referenced, and a
// delta resume from the stale cursor would never re-send them.
func refusesScope(code string) bool {
	switch code {
	case wire.ErrorVerificationUnavailable, wire.ErrorScopeLimit, wire.ErrorScopeRateLimit:
		return true
	default:
		return false
	}
}

// failureClass sorts a scope-tagged error code for the retry strategy. An outage and a rate
// limit clear with time. A bad or revoked key needs an operator, and a count bound clears
// only when another scope leaves, so those retry on the extended curve.
func failureClass(code string) retry.FailureClass {
	switch code {
	case wire.ErrorVerificationUnavailable, wire.ErrorScopeRateLimit:
		return retry.Normal
	default:
		return retry.Unexpected
	}
}
