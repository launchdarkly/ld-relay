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
}

type Rotator struct {
	loggers ldlog.Loggers

	// There is only one mobile key active at a given time.
	primaryMobileKey config.MobileKey

	// deprecatedMobileKeys stores mobile keys being phased out with a grace period, keyed
	// by credential value with the associated expiry time. StepTime walks this map and emits
	// an expiration once a key's grace period passes, mirroring deprecatedSdkKeys.
	deprecatedMobileKeys map[config.MobileKey]time.Time

	// There is only one environment ID active at a given time, and it won't actually be rotated. The mechanism is
	// here to allow setting it in a deferred manner.
	primaryEnvironmentID config.EnvironmentID

	// There can be multiple SDK keys active at a given time, but only one is the anchor.
	anchorKey config.SDKKey

	// Deprecated keys are stored in a map with a started timer for each key representing the deprecation period.
	// Upon expiration, they are removed.
	deprecatedSdkKeys map[config.SDKKey]time.Time

	// Consumed by ReconcileCredentials API
	acceptedSDKKeys    map[config.SDKKey]*acceptedKeyInfo
	acceptedMobileKeys map[config.MobileKey]*acceptedKeyInfo

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
		loggers:              loggers,
		deprecatedSdkKeys:    make(map[config.SDKKey]time.Time),
		deprecatedMobileKeys: make(map[config.MobileKey]time.Time),
		acceptedSDKKeys:      make(map[config.SDKKey]*acceptedKeyInfo),
		acceptedMobileKeys:   make(map[config.MobileKey]*acceptedKeyInfo),
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
			r.acceptedSDKKeys[cred] = &acceptedKeyInfo{}
		case config.MobileKey:
			r.primaryMobileKey = cred
			r.acceptedMobileKeys[cred] = &acceptedKeyInfo{}
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

// PrimaryCredentials returns the primary (non-deprecated) credentials.
func (r *Rotator) PrimaryCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.primaryCredentials()
}

// primaryCredentials returns every accepted, non-deprecated credential: all accepted SDK keys, all
// accepted mobile keys, and the environment ID. The primary SDK key and primary mobile key are always
// present in the accepted-set maps (maintained by Initialize, the legacy rotation path, and Reconcile)
// and are never left marked deprecated, so a plain pass over the maps already includes them.
func (r *Rotator) primaryCredentials() []SDKCredential {
	creds := make([]SDKCredential, 0, len(r.acceptedSDKKeys)+len(r.acceptedMobileKeys)+1)

	for key := range r.acceptedSDKKeys {
		if _, deprecated := r.deprecatedSdkKeys[key]; deprecated {
			continue
		}
		creds = append(creds, key)
	}
	for key := range r.acceptedMobileKeys {
		if _, deprecated := r.deprecatedMobileKeys[key]; deprecated {
			continue
		}
		creds = append(creds, key)
	}
	if r.primaryEnvironmentID.Defined() {
		creds = append(creds, r.primaryEnvironmentID)
	}
	return creds
}

func (r *Rotator) deprecatedCredentials() []SDKCredential {
	deprecated := make([]SDKCredential, 0, len(r.deprecatedSdkKeys)+len(r.deprecatedMobileKeys))
	for key := range r.deprecatedSdkKeys {
		deprecated = append(deprecated, key)
	}
	for key := range r.deprecatedMobileKeys {
		deprecated = append(deprecated, key)
	}
	return deprecated
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

	// TEMPORARY (legacy rotation path): keys deprecated via RotateWithGrace live in the
	// deprecatedSdkKeys / deprecatedMobileKeys buckets, which the reconcile path never populates. Once
	// the legacy path is removed (SDK-2603) these buckets are always empty; delete this line and the
	// deprecatedCredentials helper, leaving only the accepted-with-expiry logic below.
	out := r.deprecatedCredentials()

	for key, info := range r.acceptedSDKKeys {
		if info.expiry != nil && key != r.anchorKey {
			out = append(out, key)
		}
	}
	return out
}

// AllCredentials returns the primary and deprecated credentials as one list.
func (r *Rotator) AllCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append(r.primaryCredentials(), r.deprecatedCredentials()...)
}

// Rotate sets a new primary credential while revoking the previous.
func (r *Rotator) Rotate(cred SDKCredential) {
	r.RotateWithGrace(cred, nil)
}

// GracePeriod represents a grace period (or deprecation period) within which
// a particular SDK key is still valid, pending revocation.
type GracePeriod struct {
	// The SDK key that is being deprecated.
	key config.SDKKey
	// When the key will expire.
	expiry time.Time
	// The current timestamp.
	now time.Time
}

// Expired returns true if the key has already expired.
func (g *GracePeriod) Expired() bool {
	return g.now.After(g.expiry)
}

// NewGracePeriod constructs a new grace period. The current time must be provided in order to
// determine if the credential is already expired.
func NewGracePeriod(key config.SDKKey, expiry time.Time, now time.Time) *GracePeriod {
	return &GracePeriod{key, expiry, now}
}

// RotateWithGrace sets a new primary credential while deprecating the previous one. When grace is nil
// the outgoing credential is immediately revoked. It is invalid to specify a grace period for an
// environment ID. For mobile keys, a non-nil grace period stores the expiry for the outgoing key;
// the cleanup ticker is responsible for acting on it.
func (r *Rotator) RotateWithGrace(primary SDKCredential, grace *GracePeriod) {
	switch primary := primary.(type) {
	case config.SDKKey:
		r.updateSDKKey(primary, grace)
	case config.MobileKey:
		r.updateMobileKey(primary, grace)
	case config.EnvironmentID:
		if grace != nil {
			panic("programmer error: environment IDs do not support deprecation")
		}
		r.updateEnvironmentID(primary)
	}
}

func (r *Rotator) updateEnvironmentID(envID config.EnvironmentID) {
	if envID == r.EnvironmentID() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.primaryEnvironmentID
	r.primaryEnvironmentID = envID
	r.additions = append(r.additions, envID)
	if previous.Defined() {
		r.loggers.Infof("Environment ID %s was rotated, new environment ID is %s", r.primaryEnvironmentID, envID)
		r.expirations = append(r.expirations, previous)
	} else {
		r.loggers.Infof("New environment ID is %s", envID)
	}
}

// updateMobileKey sets a new primary mobile key. When grace is nil the outgoing key is
// immediately revoked; when non-nil its expiry is stored in deprecatedMobileKeys for the
// cleanup ticker (StepTime) to act on.
func (r *Rotator) updateMobileKey(mobileKey config.MobileKey, grace *GracePeriod) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if mobileKey == r.primaryMobileKey {
		return
	}
	previous := r.primaryMobileKey
	r.primaryMobileKey = mobileKey
	// Keep the accepted-set map (the source of truth for PrimaryCredentials) consistent with the
	// legacy rotation path.
	if _, ok := r.acceptedMobileKeys[mobileKey]; !ok {
		r.acceptedMobileKeys[mobileKey] = &acceptedKeyInfo{}
	}
	delete(r.deprecatedMobileKeys, mobileKey)
	r.additions = append(r.additions, mobileKey)
	if !previous.Defined() {
		r.loggers.Infof("New primary mobile key is %s", mobileKey.Masked())
		return
	}
	if grace == nil {
		delete(r.acceptedMobileKeys, previous)
		r.expirations = append(r.expirations, previous)
		r.loggers.Infof("Mobile key %s was rotated, new primary mobile key is %s", previous.Masked(), mobileKey.Masked())
		return
	}
	if grace.Expired() {
		delete(r.acceptedMobileKeys, previous)
		r.loggers.Infof("Deprecated mobile key %s already expired at %v; revoking immediately", previous.Masked(), grace.expiry)
		r.expirations = append(r.expirations, previous)
		return
	}
	r.deprecatedMobileKeys[previous] = grace.expiry
	r.loggers.Infof("Mobile key %s was marked for deprecation with an expiry at %v, new primary mobile key is %s",
		previous.Masked(), grace.expiry, mobileKey.Masked())
}

func (r *Rotator) swapAnchor(newKey config.SDKKey) config.SDKKey {
	if newKey == r.anchorKey {
		// There's no swap to be done, we already are using this as the anchor.
		return ""
	}
	previous := r.anchorKey
	r.anchorKey = newKey
	// Keep the accepted-set map (the source of truth for PrimaryCredentials) consistent: the new
	// anchor is accepted and is no longer deprecated, even if it was being phased out before. Mirrors
	// updateMobileKey for mobile keys.
	if _, ok := r.acceptedSDKKeys[newKey]; !ok {
		r.acceptedSDKKeys[newKey] = &acceptedKeyInfo{}
	}
	delete(r.deprecatedSdkKeys, newKey)
	r.additions = append(r.additions, newKey)
	r.loggers.Infof("New anchor SDK key is %s", newKey.Masked())

	return previous
}

func (r *Rotator) immediatelyRevoke(key config.SDKKey) {
	if key.Defined() {
		delete(r.acceptedSDKKeys, key)
		r.expirations = append(r.expirations, key)
		r.loggers.Infof("SDK key %s has been immediately revoked", key.Masked())
	}
}

func (r *Rotator) updateSDKKey(sdkKey config.SDKKey, grace *GracePeriod) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Previous will only be .Defined() if there was a previous anchor key.
	previous := r.swapAnchor(sdkKey)

	// If there's no deprecation notice, then the previous key (if any) needs to be immediately revoked so it doesn't
	// hang around forever. This case is also true when there is a grace period, but we need to inspect the grace period
	// in order to find out if immediate revocation is necessary.
	if grace == nil {
		r.immediatelyRevoke(previous)
		return
	}

	if previousExpiry, ok := r.deprecatedSdkKeys[grace.key]; ok {
		if previousExpiry != grace.expiry {
			r.loggers.Warnf("SDK key %s was marked for deprecation with an expiry at %v, but it was previously deprecated with an expiry at %v. The previous expiry will be used. ", grace.key.Masked(), grace.expiry, previousExpiry)
		}
		// When a key is deprecated by LD, it will stick around in the deprecated field of the message until something
		// else is deprecated. This means that if a key is rotated *without* a deprecation period set for the previous key,
		// then we'll receive that new primary key but the deprecation message will be stale - it'll be referring to the
		// last time a key was rotated with a deprecation period. We detect this case here (since we already saw the
		// deprecation message in our map) and ensure the previous key is revoked.
		r.immediatelyRevoke(previous)
		return
	}

	if grace.Expired() {
		r.loggers.Infof("Deprecated SDK key %s already expired at %v; revoking the previous key immediately", grace.key.Masked(), grace.expiry)
		r.immediatelyRevoke(previous)
		return
	}

	r.loggers.Infof("SDK key %s was marked for deprecation with an expiry at %v", grace.key.Masked(), grace.expiry)
	r.deprecatedSdkKeys[grace.key] = grace.expiry

	if grace.key != previous {
		r.loggers.Infof("Deprecated SDK key %s was not previously managed by Relay", grace.key.Masked())
		r.additions = append(r.additions, grace.key)
	}
}

func (r *Rotator) expireSDKKey(sdkKey config.SDKKey) {
	r.loggers.Infof("Deprecated SDK key %s has expired and is no longer valid for authentication", sdkKey.Masked())
	delete(r.deprecatedSdkKeys, sdkKey)
	delete(r.acceptedSDKKeys, sdkKey)
	r.expirations = append(r.expirations, sdkKey)
}

// expireMobileKey drops a mobile key from both the deprecated grace map and the accepted set, then
// queues its expiration. Deleting from acceptedMobileKeys is load-bearing: PrimaryCredentials derives
// from that map, so an expired key would otherwise linger as a primary credential. Mirrors expireSDKKey.
func (r *Rotator) expireMobileKey(mobileKey config.MobileKey) {
	r.loggers.Infof("Deprecated mobile key %s has expired and is no longer valid for authentication", mobileKey.Masked())
	delete(r.deprecatedMobileKeys, mobileKey)
	delete(r.acceptedMobileKeys, mobileKey)
	r.expirations = append(r.expirations, mobileKey)
}

// StepTime provides the current time to the Rotator, allowing it to compute the set of additions and expirations
// for the tracked credentials since the last time this method was called.
//
// It enforces expiry from both expiry mechanisms, for both SDK and mobile keys:
//   - The legacy grace-period maps (deprecatedSdkKeys / deprecatedMobileKeys), populated by the
//     RotateWithGrace path, where the expiry lives in the map value.
//   - The reconcile path, where per-key expiry is stored as data on the accepted entry
//     (acceptedKeyInfo.expiry); a nil expiry means the key is permanent and is never expired here.
//
// Expiry is strict (now strictly after the expiry timestamp), consistent across all four loops.
func (r *Rotator) StepTime(now time.Time) (additions []SDKCredential, expirations []SDKCredential) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Legacy grace-period deprecations (RotateWithGrace path).
	for key, expiry := range r.deprecatedSdkKeys {
		if now.After(expiry) {
			r.expireSDKKey(key)
		}
	}
	for key, expiry := range r.deprecatedMobileKeys {
		if now.After(expiry) {
			r.expireMobileKey(key)
		}
	}

	// Reconcile-path per-key expiry, stored on the accepted entry. The anchor and primary mobile key
	// carry a nil expiry, so this never drops them.
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
// key no longer desired is dropped and queued as an expiration. Either way the key is cleared from the
// deprecated map — a key the set accepts is not deprecated, and a key it revokes is gone. Per-key
// expiry is stored as data on the accepted entry; the cleanup ticker is what later acts on it. The
// caller must hold the write lock.
func reconcileAcceptedKeys[K reconcilableKey](
	desired map[K]*time.Time,
	accepted map[K]*acceptedKeyInfo,
	deprecated map[K]time.Time,
	additions *[]SDKCredential,
	expirations *[]SDKCredential,
	loggers ldlog.Loggers,
	kind string,
) {
	// First pass: walk every key the set wants us to accept. If we already accept it, just refresh
	// its expiry; if it's new, start accepting it and queue it as an addition. Either way, a desired
	// key can't also be deprecated, so clear any stale deprecation for it.
	for key, expiry := range desired {
		if info, ok := accepted[key]; ok {
			info.expiry = expiry
		} else {
			accepted[key] = &acceptedKeyInfo{expiry: expiry}
			*additions = append(*additions, key)
			loggers.Infof("%s %s is now accepted", kind, key.Masked())
		}
		delete(deprecated, key)
	}
	// Second pass: walk every key we currently accept and drop the ones the set no longer wants.
	// Keys still desired were handled above, so skip them; the rest are revoked outright (removed
	// from both maps) and queued as expirations.
	for key := range accepted {
		if _, ok := desired[key]; ok {
			continue
		}
		delete(accepted, key)
		delete(deprecated, key)
		*expirations = append(*expirations, key)
		loggers.Infof("%s %s is no longer accepted and has been revoked", kind, key.Masked())
	}
}

// reconcileSDKKeys diffs the desired SDK keys against the accepted set and applies the result via
// reconcileAcceptedKeys. The anchor is always accepted and permanent, regardless of any expiry the
// payload may carry for it. The caller must hold the write lock.
func (r *Rotator) reconcileSDKKeys(set AcceptedSet, anchor config.SDKKey, now time.Time) {
	desired := make(map[config.SDKKey]*time.Time, len(set.sdkKeys))
	for key, expiry := range set.sdkKeys {
		if expiry != nil && !now.Before(*expiry) {
			continue // already expired; treat as absent
		}
		desired[key] = expiry
	}
	desired[anchor] = nil
	reconcileAcceptedKeys(desired, r.acceptedSDKKeys, r.deprecatedSdkKeys, &r.additions, &r.expirations, r.loggers, "SDK key")
	r.anchorKey = anchor
}

// reconcileMobileKeys mirrors reconcileSDKKeys for mobile keys. The primary mobile key — the wire's
// singular mobKey, used where one mobile key is required (e.g. event forwarding) — is always accepted
// and permanent; an empty value means the set declared no mobile key. The caller must hold the lock.
func (r *Rotator) reconcileMobileKeys(set AcceptedSet, now time.Time) {
	desired := make(map[config.MobileKey]*time.Time, len(set.mobileKeys))
	for key, expiry := range set.mobileKeys {
		if expiry != nil && !now.Before(*expiry) {
			continue // already expired; treat as absent
		}
		desired[key] = expiry
	}
	if set.primaryMobileKey.Defined() {
		desired[set.primaryMobileKey] = nil
	}
	reconcileAcceptedKeys(desired, r.acceptedMobileKeys, r.deprecatedMobileKeys, &r.additions, &r.expirations, r.loggers, "Mobile key")
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
