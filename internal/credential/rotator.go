package credential

import (
	"sync"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
)

// acceptedKeyInfo holds per-key metadata for the accepted-set maps.
type acceptedKeyInfo struct {
	expiry *time.Time // nil = permanent
	key    *string    // wire "key" identifier — non-secret human-readable name; nil when absent
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
	acceptedSDKKeys map[config.SDKKey]acceptedKeyInfo

	// acceptedMobileKeys is the full set of accepted mobile keys with optional per-key expiry.
	// A nil expiry means the key is permanent.
	acceptedMobileKeys map[config.MobileKey]acceptedKeyInfo

	expirations []SDKCredential
	additions   []SDKCredential

	mu sync.RWMutex
}

type InitialCredentials struct {
	SDKKey        config.SDKKey
	MobileKey     config.MobileKey
	EnvironmentID config.EnvironmentID
}

// NewRotator constructs a rotator with the provided loggers. A new rotator
// contains no credentials and can optionally be initialized via Initialize.
func NewRotator(loggers ldlog.Loggers) *Rotator {
	r := &Rotator{
		loggers:            loggers,
		acceptedSDKKeys:    make(map[config.SDKKey]acceptedKeyInfo),
		acceptedMobileKeys: make(map[config.MobileKey]acceptedKeyInfo),
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
			r.acceptedSDKKeys[cred] = acceptedKeyInfo{}
		case config.MobileKey:
			r.primaryMobileKey = cred
			r.acceptedMobileKeys[cred] = acceptedKeyInfo{}
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
// cleanup ticker drops the key once it elapses.) EnvContext.GetDeprecatedCredentials delegates here to
// populate the status endpoint's expiringSdkKey field.
//
// Mobile keys are deliberately not returned even though they expire the same way SDK keys do — carried
// as per-key expiry and dropped by the same cleanup ticker. They are omitted only because the status
// endpoint has no expiringMobileKey field to populate, not because mobile-key expiry is unimplemented.
func (r *Rotator) DeprecatedCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []SDKCredential
	for key, info := range r.acceptedSDKKeys {
		if info.expiry != nil && key != r.anchorKey {
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
// entry (acceptedKeyInfo.expiry); a nil expiry means the key is permanent and is never expired here.
//
// Expiry happens strictly after a key's expiry timestamp.
func (r *Rotator) StepTime(now time.Time) (additions []SDKCredential, expirations []SDKCredential) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key, info := range r.acceptedSDKKeys {
		if info.expiry != nil && now.After(*info.expiry) {
			r.expireSDKKey(key)
		}
	}
	for key, info := range r.acceptedMobileKeys {
		if info.expiry != nil && now.After(*info.expiry) {
			r.expireMobileKey(key)
		}
	}

	additions, expirations = r.additions, r.expirations
	r.additions = nil
	r.expirations = nil
	return additions, expirations
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
func (r *Rotator) Reconcile(set AcceptedSet, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.reconcileSDKKeys(set, set.anchor, now)
	r.reconcileMobileKeys(set, now)
	r.reconcileEnvironmentID(set)
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
	desired map[K]acceptedKeyInfo,
	accepted map[K]acceptedKeyInfo,
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
func (r *Rotator) reconcileSDKKeys(set AcceptedSet, anchor config.SDKKey, now time.Time) {
	desired := make(map[config.SDKKey]acceptedKeyInfo, len(set.sdkKeys))
	for key, info := range set.sdkKeys {
		if info.expiry != nil && !now.Before(*info.expiry) {
			continue // already expired; treat as absent
		}
		desired[key] = info
	}
	reconcileAcceptedKeys(desired, r.acceptedSDKKeys, &r.additions, &r.expirations, r.loggers, "SDK key")
	r.anchorKey = anchor
}

// reconcileMobileKeys mirrors reconcileSDKKeys for mobile keys. The set is trusted as well-formed:
// when a primary mobile key is designated, the builder guarantees it is present and permanent
// (WithPrimaryMobileKey forces a nil expiry). An empty primary means the set declared no mobile key.
// The caller must hold the lock.
func (r *Rotator) reconcileMobileKeys(set AcceptedSet, now time.Time) {
	desired := make(map[config.MobileKey]acceptedKeyInfo, len(set.mobileKeys))
	for key, info := range set.mobileKeys {
		if info.expiry != nil && !now.Before(*info.expiry) {
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
