// Package store holds one environment's objects and the per-scope membership over them.
//
// Object content arrives once on the base scope however many credentials may see it. Each
// registered credential is a scope whose membership is a set of references into that base.
// This package keeps both, and the reverse of the membership index, so a single content
// change resolves to the scopes that care about it with one map lookup rather than a walk
// over every scope's membership.
//
// Content and membership are separate planes that never cross. Membership changes only
// through refs or wholesale scope removal; content changes only through object messages. In
// particular, evicting an object leaves every scope's membership alone.
//
// Raw bytes are the currency. Both stream protocols, all polling, and the database writes
// splice the object's JSON as it arrived; only evaluation and event summarization need a
// parsed model, so parsing happens on first need and is memoized per record.
package store
