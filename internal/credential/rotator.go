package credential

import (
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
)

// AcceptedKey is the metadata for one accepted credential: its optional expiry and its optional wire
// identifier. The credential value itself is the map key wherever AcceptedKey is stored.
type AcceptedKey struct {
	// Expiry is the key's expiry. A nil expiry means the key is permanent.
	Expiry *time.Time
	// Key is the non-secret wire identifier, a human-readable name. Nil when the source carried none
	// (manual configuration, or an old-format payload predating concurrent keys).
	Key *string
}

// AcceptedKeySet is a point-in-time snapshot of an environment's full accepted set, returned by
// Rotator.AcceptedKeys. Server and Mobile are keyed by credential value; Anchor and PrimaryMobile
// name the designated keys within them. Reads are taken under one lock, so the fields agree.
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

// AnchorKey returns the anchor SDK key, which owns the upstream connection.
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

// DeprecatedCredentials returns every accepted SDK key, other than the anchor, that carries an
// expiry. It does not return mobile keys, which expire the same way.
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

// AllCredentials returns every accepted credential: SDK keys, mobile keys, and the environment ID.
func (r *Rotator) AllCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.allCredentials()
}

// IsAccepted reports whether cred is currently one of the environment's accepted credentials: an
// accepted SDK key, an accepted mobile key (including one carrying a future expiry, which still
// authenticates until the cleanup ticker drops it), or the environment ID. Any other credential type is
// never accepted.
//
// This answers the same question as membership in AllCredentials, by direct map lookup, so callers on
// request paths do not allocate a credential slice per call.
func (r *Rotator) IsAccepted(cred SDKCredential) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	switch cred := cred.(type) {
	case config.SDKKey:
		_, ok := r.acceptedSDKKeys[cred]
		return ok
	case config.MobileKey:
		_, ok := r.acceptedMobileKeys[cred]
		return ok
	case config.EnvironmentID:
		return r.primaryEnvironmentID.Defined() && cred == r.primaryEnvironmentID
	default:
		return false
	}
}

func (r *Rotator) expireSDKKey(sdkKey config.SDKKey) {
	r.loggers.Infof("Deprecated SDK key %s has expired and is no longer valid for authentication", sdkKey.Masked())
	delete(r.acceptedSDKKeys, sdkKey)
	r.expirations = append(r.expirations, sdkKey)
}

// expireMobileKey drops a mobile key from the accepted set and queues its expiration. Deleting from
// acceptedMobileKeys is load-bearing: AllCredentials derives from that map.
func (r *Rotator) expireMobileKey(mobileKey config.MobileKey) {
	r.loggers.Infof("Deprecated mobile key %s has expired and is no longer valid for authentication", mobileKey.Masked())
	delete(r.acceptedMobileKeys, mobileKey)
	r.expirations = append(r.expirations, mobileKey)
}

// StepTime provides the current time to the Rotator, so it can compute the additions and expirations
// for the tracked credentials since the last call. It enforces per-key expiry for SDK and mobile keys,
// strictly after a key's expiry timestamp.
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

// ReconcileResult signals state changes the caller must apply itself, rather than through the
// StepTime-driven addCredential and removeCredential flow.
//
// AnchorChange is non-nil when the SDK anchor changed. The caller drives the re-anchor sequence; see
// envContextImpl.reanchor.
//
// MobilePrimaryRepoint is non-nil when the primary mobile key changed to a key already accepted. Such
// a key is not in additions, so addCredential does not repoint the event dispatcher for it and the
// caller must do so.
type ReconcileResult struct {
	AnchorChange         *AnchorChange
	MobilePrimaryRepoint *config.MobileKey
}

// AnchorChange describes an SDK anchor transition produced by Reconcile.
//
// NewAnchorPreviouslyAccepted is false when the new anchor is a brand-new key, so the re-anchor must
// register that key's credential mappings. It is true when the key was already accepted, so the
// mappings already exist and a client may too. See envContextImpl.reanchor.
type AnchorChange struct {
	PreviousAnchor              config.SDKKey
	NewAnchor                   config.SDKKey
	NewAnchorPreviouslyAccepted bool
}

// Reconcile updates the rotator to match set. It queues additions and expirations, which the next
// StepTime call drains. Keys newly present are accepted; keys no longer present are revoked. An
// undefined environment ID leaves the current one unchanged. Per-key expiry is stored on the accepted
// entry, and StepTime acts on it.
//
// set is assumed well-formed: AcceptedSetBuilder.Build already guaranteed a designated anchor.
//
// Reconcile does NOT flip the anchor pointer. The returned AnchorChange signals the change; the
// caller drives the re-anchor and then calls CommitAnchor. A brand-new anchor is also stripped from
// additions, because reanchor registers that key's mappings itself.
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
		// The anchor is a new key, so reconcileAcceptedKeys appended it to r.additions. Strip it:
		// reanchor registers this key's mappings, so draining the addition would register them twice.
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
		// The new primary is not in additions, so addCredential's gate will not fire for it.
		m := newMobile
		result.MobilePrimaryRepoint = &m
	}

	r.reconcileEnvironmentID(set)

	return result
}

// CommitAnchor moves the rotator's SDK anchor pointer to key. The caller invokes it once the new
// anchor's client is ready. Until then the anchor stays on the previous key, so GetClient() returns
// the still-serving old client. Initialize and CommitAnchor are the only paths that move the pointer.
func (r *Rotator) CommitAnchor(key config.SDKKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.anchorKey = key
}

// RevertAnchorChange undoes the accepted-set effects of a failed re-anchor. CommitAnchor was never
// called, so the anchor pointer still names the previous anchor. This method realigns the accepted set.
//
// If this reconcile revoked the previous anchor outright, RevertAnchorChange re-admits that key as a
// permanent key with no expiry. The re-admission discards the key's previous expiry, because that key
// is still the anchor and still serving. A previous anchor that is only grace-demoted stays accepted
// and keeps its expiry. The new anchor is dropped only if that key was brand new.
func (r *Rotator) RevertAnchorChange(change AnchorChange) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Only re-admit a defined previous anchor. The rotator holds defined keys only, and an env that
	// gains its first SDK key has an undefined previous anchor.
	if change.PreviousAnchor.Defined() {
		if _, stillAccepted := r.acceptedSDKKeys[change.PreviousAnchor]; !stillAccepted {
			r.acceptedSDKKeys[change.PreviousAnchor] = AcceptedKey{}
		}
	}
	if !change.NewAnchorPreviouslyAccepted {
		delete(r.acceptedSDKKeys, change.NewAnchor)
	}
}

// reconcilableKey constrains the generic reconcile helper to a comparable SDKCredential, so the helper
// can both key a map with it and log it.
type reconcilableKey interface {
	comparable
	SDKCredential
}

// reconcileAcceptedKeys diffs the desired keys against the currently-accepted ones, for SDK and
// mobile keys alike: a desired key not yet accepted is queued as an addition; an accepted key no
// longer desired is dropped and queued as an expiration. The caller must hold the write lock.
func reconcileAcceptedKeys[K reconcilableKey](
	desired map[K]AcceptedKey,
	accepted map[K]AcceptedKey,
	additions *[]SDKCredential,
	expirations *[]SDKCredential,
	loggers ldlog.Loggers,
	kind string,
) {
	// Writing the desired entry over the accepted one refreshes the expiry and the wire identifier. It
	// clears the identifier when the new payload carries none, so a stale name never lingers in /status.
	for key, want := range desired {
		if _, ok := accepted[key]; !ok {
			*additions = append(*additions, key)
			loggers.Infof("%s %s is now accepted", kind, key.Masked())
		}
		accepted[key] = want
	}
	// Drop every accepted key the set no longer wants. Those keys are revoked outright.
	for key := range accepted {
		if _, ok := desired[key]; ok {
			continue
		}
		delete(accepted, key)
		*expirations = append(*expirations, key)
		loggers.Infof("%s %s is no longer accepted and has been revoked", kind, key.Masked())
	}
}

// reconcileSDKKeys diffs the desired SDK keys against the accepted set via reconcileAcceptedKeys. The
// anchor is present and permanent, per AcceptedSetBuilder. The caller must hold the write lock.
func (r *Rotator) reconcileSDKKeys(set AcceptedSet, now time.Time) {
	desired := make(map[config.SDKKey]AcceptedKey, len(set.sdkKeys))
	for key, info := range set.sdkKeys {
		if info.Expiry != nil && now.After(*info.Expiry) {
			continue // already expired; treat as absent
		}
		desired[key] = info
	}
	reconcileAcceptedKeys(desired, r.acceptedSDKKeys, &r.additions, &r.expirations, r.loggers, "SDK key")
}

// reconcileMobileKeys does for mobile keys what reconcileSDKKeys does for SDK keys. An empty primary
// means the set declared no mobile key. The caller must hold the write lock.
func (r *Rotator) reconcileMobileKeys(set AcceptedSet, now time.Time) {
	desired := make(map[config.MobileKey]AcceptedKey, len(set.mobileKeys))
	for key, info := range set.mobileKeys {
		if info.Expiry != nil && now.After(*info.Expiry) {
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

// AcceptedKeys returns a snapshot of the full accepted set. See AcceptedKeySet.
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
