package credential

import (
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
)

// AcceptedKey is the metadata for one accepted credential: its optional expiry and optional wire
// "key" identifier. The credential value itself is the map key wherever AcceptedKey is stored — the
// rotator's accepted-key maps, the builder's AcceptedSet, and the AcceptedKeySet returned by
// AcceptedKeys.
type AcceptedKey struct {
	// Expiry is the key's expiry. A nil expiry means the key is permanent.
	Expiry *time.Time
	// Key is the non-secret wire "key" identifier — a human-readable name. Nil when the source carried
	// none (manual configuration, or an old-format payload predating concurrent keys).
	Key *string
}

// AcceptedKeySet is a point-in-time snapshot of an environment's full accepted credential set,
// returned by Rotator.AcceptedKeys. Server and Mobile are keyed by credential value (the secret);
// the value AcceptedKey carries that key's metadata. Anchor and PrimaryMobile name the designated
// keys within Server and Mobile. The status endpoint maps Server/Mobile to the sdkKeys[]/mobileKeys[]
// arrays and uses Anchor to mark the anchor entry. Reads of the maps and the designations are taken
// under a single lock, so they are mutually consistent.
type AcceptedKeySet struct {
	Server        map[config.SDKKey]AcceptedKey
	Mobile        map[config.MobileKey]AcceptedKey
	Anchor        config.SDKKey
	PrimaryMobile config.MobileKey
}

type Rotator struct {
	loggers ldlog.Loggers

	// There is only one mobile key active at a given time.
	primaryMobileKey config.MobileKey

	// There is only one environment ID active at a given time, and it won't actually be rotated. The mechanism is
	// here to allow setting it in a deferred manner.
	primaryEnvironmentID config.EnvironmentID

	// There can be multiple SDK keys active at a given time, but only one is the anchor.
	anchorKey config.SDKKey

	// acceptedSDKKeys is the full set of accepted SDK keys with optional per-key expiry.
	// A nil expiry means the key is permanent. The anchor is always present with a nil expiry.
	acceptedSDKKeys map[config.SDKKey]AcceptedKey

	// acceptedMobileKeys is the full set of accepted mobile keys with optional per-key expiry.
	// A nil expiry means the key is permanent.
	acceptedMobileKeys map[config.MobileKey]AcceptedKey

	expirations []SDKCredential
	additions   []SDKCredential

	mu sync.RWMutex
}

// NewRotator constructs a rotator with the provided loggers. A new rotator
// contains no credentials and can optionally be initialized via Initialize.
func NewRotator(loggers ldlog.Loggers) *Rotator {
	r := &Rotator{
		loggers:            loggers,
		acceptedSDKKeys:    make(map[config.SDKKey]AcceptedKey),
		acceptedMobileKeys: make(map[config.MobileKey]AcceptedKey),
	}
	return r
}

// Initialize sets the initial credentials. Only credentials that are defined
// will be stored.
func (r *Rotator) Initialize(credentials []SDKCredential) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, cred := range credentials {
		if !cred.Defined() {
			continue
		}
		switch cred := cred.(type) {
		case config.SDKKey:
			r.anchorKey = cred
			r.acceptedSDKKeys[cred] = AcceptedKey{}
		case config.MobileKey:
			r.primaryMobileKey = cred
			r.acceptedMobileKeys[cred] = AcceptedKey{}
		case config.EnvironmentID:
			r.primaryEnvironmentID = cred
		}
	}
}

// MobileKey returns the primary mobile key.
func (r *Rotator) MobileKey() config.MobileKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.primaryMobileKey
}

// AnchorKey returns the anchor SDK key — the key that owns the upstream connection.
func (r *Rotator) AnchorKey() config.SDKKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.anchorKey
}

// EnvironmentID returns the environment ID.
func (r *Rotator) EnvironmentID() config.EnvironmentID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.primaryEnvironmentID
}

// allCredentials returns every accepted credential. Expiring keys are included until the
// cleanup ticker drops them (StepTime). The caller must hold at least a read lock.
func (r *Rotator) allCredentials() []SDKCredential {
	creds := make([]SDKCredential, 0, len(r.acceptedSDKKeys)+len(r.acceptedMobileKeys)+1)
	for key := range r.acceptedSDKKeys {
		creds = append(creds, key)
	}
	for key := range r.acceptedMobileKeys {
		creds = append(creds, key)
	}
	if r.primaryEnvironmentID.Defined() {
		creds = append(creds, r.primaryEnvironmentID)
	}
	return creds
}

// DeprecatedCredentials returns the SDK keys being phased out — every accepted SDK key, other than the
// anchor, that carries a future expiry. (Per-key expiry is stored as data on the accepted entry; the
// cleanup ticker drops the key once it elapses.)
//
// Mobile keys are deliberately not returned even though they expire the same way SDK keys do — carried
// as per-key expiry and dropped by the same cleanup ticker.
func (r *Rotator) DeprecatedCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []SDKCredential
	for key, info := range r.acceptedSDKKeys {
		if info.Expiry != nil && key != r.anchorKey {
			out = append(out, key)
		}
	}
	return out
}

// AllCredentials returns every accepted credential: every accepted SDK key, every accepted mobile
// key (including those carrying a future expiry — they still authenticate until the cleanup ticker
// drops them), and the environment ID.
func (r *Rotator) AllCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.allCredentials()
}

func (r *Rotator) expireSDKKey(sdkKey config.SDKKey) {
	r.loggers.Infof("Deprecated SDK key %s has expired and is no longer valid for authentication", sdkKey.Masked())
	delete(r.acceptedSDKKeys, sdkKey)
	r.expirations = append(r.expirations, sdkKey)
}

// expireMobileKey drops a mobile key from the accepted set and queues its expiration.
// Deleting from acceptedMobileKeys is load-bearing: AllCredentials derives from that map,
// so an expired key would otherwise linger as an accepted credential. Mirrors expireSDKKey.
func (r *Rotator) expireMobileKey(mobileKey config.MobileKey) {
	r.loggers.Infof("Deprecated mobile key %s has expired and is no longer valid for authentication", mobileKey.Masked())
	delete(r.acceptedMobileKeys, mobileKey)
	r.expirations = append(r.expirations, mobileKey)
}

// StepTime provides the current time to the Rotator, allowing it to compute the set of additions and
// expirations for the tracked credentials since the last time this method was called.
//
// It enforces per-key expiry for both SDK and mobile keys: expiry is stored as data on the accepted
// entry (AcceptedKey.Expiry); a nil expiry means the key is permanent and is never expired here.
//
// Expiry happens strictly after a key's expiry timestamp.
func (r *Rotator) StepTime(now time.Time) (additions []SDKCredential, expirations []SDKCredential) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key, info := range r.acceptedSDKKeys {
		if key == r.anchorKey {
			// Never expire the current anchor, even if a stale expiry was left on its entry (a
			// rolled-back re-anchor can). Same guard as DeprecatedCredentials.
			continue
		}
		if info.Expiry != nil && now.After(*info.Expiry) {
			r.expireSDKKey(key)
		}
	}
	for key, info := range r.acceptedMobileKeys {
		if info.Expiry != nil && now.After(*info.Expiry) {
			r.expireMobileKey(key)
		}
	}

	additions, expirations = r.additions, r.expirations
	r.additions = nil
	r.expirations = nil
	return additions, expirations
}

// ReconcileResult signals state changes that the caller must apply synchronously rather than rely
// on the normal addCredential / removeCredential flow driven by StepTime.
//
// AnchorChange is non-nil when the SDK anchor changed during Reconcile. The rotator does NOT flip
// its anchor pointer in that case — the caller must drive the synchronous re-anchor sequence
// (build the new anchor's SDK client if one does not exist, wait for Initialized, then invoke
// CommitAnchor to atomically move the pointer, then call ReplaceCredential on the event dispatcher
// and metrics publisher, then re-wire big-segment sync).
//
// MobilePrimaryRepoint is non-nil when the primary mobile key changed AND the new primary was
// already in the accepted set. In that case it does not appear in StepTime's additions list and
// addCredential's primary-mobile gate will not fire for it, so the caller must invoke
// eventDispatcher.ReplaceCredential synchronously. When nil, either the primary mobile key did not
// change, or it changed to a newly-accepted key — in which case the normal addCredential path
// handles the ReplaceCredential call via the existing gate.
type ReconcileResult struct {
	AnchorChange         *AnchorChange
	MobilePrimaryRepoint *config.MobileKey
}

// AnchorChange describes an SDK anchor transition produced by Reconcile.
//
// NewAnchorPreviouslyAccepted distinguishes the two re-anchor paths:
//   - false (the anchor is a new key): the new anchor was not previously in the accepted set. The
//     synchronous re-anchor must register the credential mappings (envStreams, handlers, connection
//     mapping), construct and initialize a new SDK client, then invoke CommitAnchor + ReplaceCredential.
//   - true (the anchor is a previously-accepted key): the new anchor was already accepted (typically
//     a former anchor still in its grace period). Its credential mappings are already registered and a
//     client may already exist; the synchronous re-anchor reuses it (or constructs one only if missing
//     — see the re-anchor sequence in env_context_impl.go), then invokes CommitAnchor + ReplaceCredential.
type AnchorChange struct {
	PreviousAnchor              config.SDKKey
	NewAnchor                   config.SDKKey
	NewAnchorPreviouslyAccepted bool
}

// Reconcile updates the rotator to match set. The set names its own anchor (the primary SDK key) and
// primary mobile key. It diffs the desired accepted set against the current one and queues additions
// and expirations (drained by the next StepTime call); keys newly present are accepted, and keys no
// longer present are revoked. Per-key expiry is stored as data on the accepted entry; the cleanup
// ticker (StepTime) is what later acts on it, dropping a key once its expiry passes. An undefined
// environment ID leaves the current one unchanged, since environments are removed via teardown
// rather than reconcile.
//
// The set is assumed well-formed: AcceptedSetBuilder.Build validates that an anchor was designated
// (and, because WithAnchor adds the key as it designates it, that the anchor is among the SDK
// keys), so Reconcile trusts what it is handed rather than re-validating.
//
// Reconcile does NOT flip the SDK anchor pointer when the anchor changes — the returned
// ReconcileResult.AnchorChange signals the change so the caller can drive the synchronous re-anchor
// sequence, then call CommitAnchor to atomically move the pointer. When the new anchor is a new key
// (NewAnchorPreviouslyAccepted == false) it is also stripped from additions so that the async
// startSDKClient invocation in addCredential does not race the synchronous client build.
func (r *Rotator) Reconcile(set AcceptedSet, now time.Time) ReconcileResult {
	r.mu.Lock()
	defer r.mu.Unlock()

	var result ReconcileResult

	previousAnchor := r.anchorKey
	newAnchor := set.anchor
	if previousAnchor != newAnchor && newAnchor.Defined() {
		_, alreadyAccepted := r.acceptedSDKKeys[newAnchor]
		result.AnchorChange = &AnchorChange{
			PreviousAnchor:              previousAnchor,
			NewAnchor:                   newAnchor,
			NewAnchorPreviouslyAccepted: alreadyAccepted,
		}
	}

	r.reconcileSDKKeys(set, now)

	if result.AnchorChange != nil && !result.AnchorChange.NewAnchorPreviouslyAccepted {
		// The anchor is a new key: reconcileAcceptedKeys just appended it to r.additions. Strip it —
		// the synchronous re-anchor sequence in env_context_impl owns the new anchor's setup
		// (credential mappings + client build + flip + ReplaceCredential). If addCredential drained this
		// addition normally, its async startSDKClient would race the synchronous build. When the anchor
		// is a previously-accepted key it was already in acceptedSDKKeys, so reconcileAcceptedKeys did
		// not add it — no strip needed.
		r.additions = slices.DeleteFunc(r.additions, func(c SDKCredential) bool { return c == newAnchor })
	}

	previousMobile := r.primaryMobileKey
	newMobile := set.primaryMobileKey
	var newMobileAlreadyAccepted bool
	if newMobile.Defined() {
		_, newMobileAlreadyAccepted = r.acceptedMobileKeys[newMobile]
	}
	r.reconcileMobileKeys(set, now)
	if previousMobile != newMobile && newMobile.Defined() && newMobileAlreadyAccepted {
		// Primary mobile key changed to a key already in the accepted set: addCredential's gate will
		// not fire for it (it's not in additions), so the caller must call ReplaceCredential itself.
		m := newMobile
		result.MobilePrimaryRepoint = &m
	}

	r.reconcileEnvironmentID(set)

	return result
}

// CommitAnchor atomically moves the rotator's SDK anchor pointer to the given key. The caller
// invokes this once the synchronous re-anchor sequence is ready to flip — i.e. after the new
// anchor's client is built and reports Initialized (when the anchor is a new key) or after confirming
// the existing client will be reused (when the anchor is a previously-accepted key). Until CommitAnchor
// is called, the rotator's anchor stays on the previous key so GetClient() returns the still-serving
// old client and the gate in addCredential does not fire for the pending new anchor.
//
// Aside from Initialize (which establishes the initial anchor), CommitAnchor is the only path that
// moves the anchor pointer: Reconcile deliberately does not flip it (see reconcileSDKKeys).
func (r *Rotator) CommitAnchor(key config.SDKKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.anchorKey = key
}

// RevertAnchorChange undoes the accepted-set effects of an AnchorChange whose synchronous re-anchor
// failed and rolled back. Because CommitAnchor was never called, the anchor pointer still names the
// previous anchor; this realigns the accepted set with it so the two don't disagree.
//
//   - If the previous anchor is a defined key that was revoked in the same reconcile (it is no longer
//     accepted — an immediate revocation rather than a grace demotion), re-admit it as a permanent
//     key, since it remains the anchor and keeps serving. If it is still accepted (grace demotion),
//     leave it and its expiry untouched. An undefined previous anchor (the env's first SDK key) is
//     never admitted.
//   - Drop the failed new anchor, but only if it was brand new; a previously-accepted key that was
//     promoted and failed stays accepted as the non-anchor key it already was.
func (r *Rotator) RevertAnchorChange(change AnchorChange) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Only re-admit a defined previous anchor. When an env gains its first SDK key, the previous anchor
	// is the empty (undefined) key — there is nothing to re-admit, and inserting "" would put an
	// undefined credential into the accepted set (the rotator otherwise only holds defined keys).
	if change.PreviousAnchor.Defined() {
		if _, stillAccepted := r.acceptedSDKKeys[change.PreviousAnchor]; !stillAccepted {
			r.acceptedSDKKeys[change.PreviousAnchor] = AcceptedKey{}
		}
	}
	if !change.NewAnchorPreviouslyAccepted {
		delete(r.acceptedSDKKeys, change.NewAnchor)
	}
}

// reconcilableKey constrains the generic reconcile helper to a comparable credential (so it can key a
// map) that is also an SDKCredential (so it can be logged and appended to the credential lists).
type reconcilableKey interface {
	comparable
	SDKCredential
}

// reconcileAcceptedKeys diffs the desired keys against the currently-accepted ones (SDK or mobile,
// same algorithm): a desired key not yet accepted is recorded and queued as an addition; an accepted
// key no longer desired is dropped and queued as an expiration. Per-key expiry is stored as data on
// the accepted entry; the cleanup ticker is what later acts on it. The caller must hold the write lock.
func reconcileAcceptedKeys[K reconcilableKey](
	desired map[K]AcceptedKey,
	accepted map[K]AcceptedKey,
	additions *[]SDKCredential,
	expirations *[]SDKCredential,
	loggers ldlog.Loggers,
	kind string,
) {
	// First pass: walk every key the set wants us to accept. Writing the desired entry over the
	// accepted one refreshes its metadata — both the expiry and the wire "key" identifier, clearing
	// the identifier when the new payload carries none so a stale name never lingers in /status. A key
	// we don't yet accept is also queued as an addition.
	for key, want := range desired {
		if _, ok := accepted[key]; !ok {
			*additions = append(*additions, key)
			loggers.Infof("%s %s is now accepted", kind, key.Masked())
		}
		accepted[key] = want
	}
	// Second pass: walk every key we currently accept and drop the ones the set no longer wants.
	// Keys still desired were handled above, so skip them; the rest are revoked outright (removed
	// from the map) and queued as expirations.
	for key := range accepted {
		if _, ok := desired[key]; ok {
			continue
		}
		delete(accepted, key)
		*expirations = append(*expirations, key)
		loggers.Infof("%s %s is no longer accepted and has been revoked", kind, key.Masked())
	}
}

// reconcileSDKKeys diffs the desired SDK keys against the accepted set and applies the result via
// reconcileAcceptedKeys. The set is trusted as well-formed: BuildAcceptedSet / the builder guarantee
// the anchor is present and permanent (WithAnchor forces a nil expiry), so no special handling is
// needed here. The caller must hold the write lock.
//
// NOTE: reconcileSDKKeys does NOT flip r.anchorKey when the anchor changes. The Reconcile caller
// signals the anchor change via ReconcileResult.AnchorChange and invokes CommitAnchor to move the
// pointer once the synchronous re-anchor sequence is ready (see Reconcile + CommitAnchor).
func (r *Rotator) reconcileSDKKeys(set AcceptedSet, now time.Time) {
	desired := make(map[config.SDKKey]AcceptedKey, len(set.sdkKeys))
	for key, info := range set.sdkKeys {
		if info.Expiry != nil && !now.Before(*info.Expiry) {
			continue // already expired; treat as absent
		}
		desired[key] = info
	}
	reconcileAcceptedKeys(desired, r.acceptedSDKKeys, &r.additions, &r.expirations, r.loggers, "SDK key")
}

// reconcileMobileKeys mirrors reconcileSDKKeys for mobile keys. The set is trusted as well-formed:
// when a primary mobile key is designated, the builder guarantees it is present and permanent
// (WithPrimaryMobileKey forces a nil expiry). An empty primary means the set declared no mobile key.
// The caller must hold the lock.
func (r *Rotator) reconcileMobileKeys(set AcceptedSet, now time.Time) {
	desired := make(map[config.MobileKey]AcceptedKey, len(set.mobileKeys))
	for key, info := range set.mobileKeys {
		if info.Expiry != nil && !now.Before(*info.Expiry) {
			continue // already expired; treat as absent
		}
		desired[key] = info
	}
	reconcileAcceptedKeys(desired, r.acceptedMobileKeys, &r.additions, &r.expirations, r.loggers, "Mobile key")
	r.primaryMobileKey = set.primaryMobileKey
}

// reconcileEnvironmentID updates the environment ID if the set carries a new one. The caller must
// hold the write lock.
func (r *Rotator) reconcileEnvironmentID(set AcceptedSet) {
	if !set.envID.Defined() || set.envID == r.primaryEnvironmentID {
		return
	}
	if r.primaryEnvironmentID.Defined() {
		r.expirations = append(r.expirations, r.primaryEnvironmentID)
	}
	r.primaryEnvironmentID = set.envID
	r.additions = append(r.additions, set.envID)
}

// AcceptedKeys returns a snapshot of the full accepted credential set — all server-side SDK keys and
// all mobile keys (anchor and primary mobile key included) — grouped by kind, along with which keys
// are the designated anchor and primary mobile. The maps and the designations are read under a single
// lock so they are mutually consistent. The status endpoint maps each group to the sdkKeys[] /
// mobileKeys[] arrays.
func (r *Rotator) AcceptedKeys() AcceptedKeySet {
	r.mu.RLock()
	defer r.mu.RUnlock()

	server := make(map[config.SDKKey]AcceptedKey, len(r.acceptedSDKKeys))
	maps.Copy(server, r.acceptedSDKKeys)
	mobile := make(map[config.MobileKey]AcceptedKey, len(r.acceptedMobileKeys))
	maps.Copy(mobile, r.acceptedMobileKeys)
	return AcceptedKeySet{
		Server:        server,
		Mobile:        mobile,
		Anchor:        r.anchorKey,
		PrimaryMobile: r.primaryMobileKey,
	}
}
