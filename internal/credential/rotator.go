package credential

import (
	"slices"
	"sync"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
)

type Rotator struct {
	loggers ldlog.Loggers

	// There is only one environment ID active at a given time, and it won't actually be rotated. The mechanism is
	// here to allow setting it in a deferred manner.
	primaryEnvironmentID config.EnvironmentID

	// There can be multiple SDK keys active at a given time, but only one is primary.
	primarySdkKey config.SDKKey

	// additionalSdkKeys is the set of concurrent SDK keys that authenticate to the same environment
	// as primarySdkKey but never open an upstream LD connection. Entries with a per-key expiry are
	// tracked in expiringAdditionalSdkKeys instead.
	additionalSdkKeys map[config.SDKKey]struct{}

	// expiringAdditionalSdkKeys tracks concurrent SDK keys that have a per-key expiry. Separate
	// from deprecatedSdkKeys so the SetAdditionalSDKKeys diff logic cannot accidentally remove the
	// predecessor of primarySdkKey.
	expiringAdditionalSdkKeys map[config.SDKKey]time.Time

	// deprecatedSdkKeys holds the predecessor of primarySdkKey during a rotation grace period. Upon
	// expiration, entries are removed. This map is owned by the RotateWithGrace path.
	deprecatedSdkKeys map[config.SDKKey]time.Time

	// Mobile key state mirrors the SDK key state above.
	primaryMobileKey             config.MobileKey
	additionalMobileKeys         map[config.MobileKey]struct{}
	expiringAdditionalMobileKeys map[config.MobileKey]time.Time
	deprecatedMobileKeys         map[config.MobileKey]time.Time

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
		loggers:                      loggers,
		additionalSdkKeys:            make(map[config.SDKKey]struct{}),
		expiringAdditionalSdkKeys:    make(map[config.SDKKey]time.Time),
		deprecatedSdkKeys:            make(map[config.SDKKey]time.Time),
		additionalMobileKeys:         make(map[config.MobileKey]struct{}),
		expiringAdditionalMobileKeys: make(map[config.MobileKey]time.Time),
		deprecatedMobileKeys:         make(map[config.MobileKey]time.Time),
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
			r.primarySdkKey = cred
		case config.MobileKey:
			r.primaryMobileKey = cred
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

// SDKKey returns the primary SDK key.
func (r *Rotator) SDKKey() config.SDKKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.primarySdkKey
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

func (r *Rotator) primaryCredentials() []SDKCredential {
	creds := slices.DeleteFunc([]SDKCredential{
		r.primarySdkKey,
		r.primaryMobileKey,
		r.primaryEnvironmentID,
	}, func(cred SDKCredential) bool {
		return !cred.Defined()
	})
	for key := range r.additionalSdkKeys {
		creds = append(creds, key)
	}
	for key := range r.additionalMobileKeys {
		creds = append(creds, key)
	}
	return creds
}

// ActiveSDKKeys returns the primary SDK key plus all additional non-deprecated SDK keys.
func (r *Rotator) ActiveSDKKeys() []config.SDKKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]config.SDKKey, 0, 1+len(r.additionalSdkKeys))
	if r.primarySdkKey.Defined() {
		keys = append(keys, r.primarySdkKey)
	}
	for k := range r.additionalSdkKeys {
		keys = append(keys, k)
	}
	return keys
}

// SetAdditionalSDKKeys synchronizes the set of concurrent SDK keys for this environment.
//
// active contains keys that are present and have no per-key expiry. expiring maps each key with a
// per-key expiry to its absolute expiration timestamp.
//
// Keys new to active or expiring are queued as additions. Keys previously tracked that are absent
// from both sets are queued as expirations immediately, without a grace period -- omission from a
// patch is treated as deletion. A key transitioning between active and expiring stays mapped; only
// its grace state changes. An expiring key whose timestamp changes across calls accepts the new
// value as authoritative.
//
// The primary SDK key is filtered out of the additional set defensively. This method does not
// touch deprecatedSdkKeys, which is owned by the RotateWithGrace path for the predecessor of
// primarySdkKey during a rotation.
func (r *Rotator) SetAdditionalSDKKeys(active []config.SDKKey, expiring map[config.SDKKey]time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	nextActive := make(map[config.SDKKey]struct{}, len(active))
	for _, k := range active {
		if !k.Defined() || k == r.primarySdkKey {
			continue
		}
		nextActive[k] = struct{}{}
	}
	nextExpiring := make(map[config.SDKKey]time.Time, len(expiring))
	for k, t := range expiring {
		if !k.Defined() || k == r.primarySdkKey {
			continue
		}
		// If the same key shows up in both sets, treat the expiring entry as authoritative -- the
		// platform explicitly attached an expiry to it.
		delete(nextActive, k)
		nextExpiring[k] = t
	}

	// Track all currently-tracked additional keys (active + expiring) so we can detect removals.
	current := make(map[config.SDKKey]struct{})
	for k := range r.additionalSdkKeys {
		current[k] = struct{}{}
	}
	for k := range r.expiringAdditionalSdkKeys {
		current[k] = struct{}{}
	}

	for k := range nextActive {
		_, wasAdditional := r.additionalSdkKeys[k]
		_, wasExpiring := r.expiringAdditionalSdkKeys[k]
		switch {
		case wasAdditional:
			// Already active.
		case wasExpiring:
			// Was expiring, now active -- move state without re-queuing (still mapped).
			delete(r.expiringAdditionalSdkKeys, k)
			r.additionalSdkKeys[k] = struct{}{}
		default:
			r.additionalSdkKeys[k] = struct{}{}
			r.additions = append(r.additions, k)
			r.loggers.Infof("Additional SDK key %s is now active", k.Masked())
		}
		delete(current, k)
	}

	for k, t := range nextExpiring {
		_, wasAdditional := r.additionalSdkKeys[k]
		previousExpiry, wasExpiring := r.expiringAdditionalSdkKeys[k]
		switch {
		case wasAdditional:
			// Was active, now has an expiry -- move into expiring, stay mapped.
			delete(r.additionalSdkKeys, k)
			r.expiringAdditionalSdkKeys[k] = t
			r.loggers.Infof("Additional SDK key %s is now expiring at %v", k.Masked(), t)
		case wasExpiring:
			// Already expiring -- accept the new timestamp (platform is authoritative).
			if previousExpiry != t {
				r.loggers.Infof("Additional SDK key %s expiry updated from %v to %v", k.Masked(), previousExpiry, t)
				r.expiringAdditionalSdkKeys[k] = t
			}
		default:
			r.expiringAdditionalSdkKeys[k] = t
			r.additions = append(r.additions, k)
			r.loggers.Infof("Additional SDK key %s registered with expiry %v", k.Masked(), t)
		}
		delete(current, k)
	}

	// Anything still in current was previously tracked but absent from the new patch -- revoke now.
	for k := range current {
		delete(r.additionalSdkKeys, k)
		delete(r.expiringAdditionalSdkKeys, k)
		r.expirations = append(r.expirations, k)
		r.loggers.Infof("Additional SDK key %s has been revoked", k.Masked())
	}
}

func (r *Rotator) deprecatedCredentials() []SDKCredential {
	total := len(r.deprecatedSdkKeys) + len(r.expiringAdditionalSdkKeys) +
		len(r.deprecatedMobileKeys) + len(r.expiringAdditionalMobileKeys)
	deprecated := make([]SDKCredential, 0, total)
	for key := range r.deprecatedSdkKeys {
		deprecated = append(deprecated, key)
	}
	for key := range r.expiringAdditionalSdkKeys {
		deprecated = append(deprecated, key)
	}
	for key := range r.deprecatedMobileKeys {
		deprecated = append(deprecated, key)
	}
	for key := range r.expiringAdditionalMobileKeys {
		deprecated = append(deprecated, key)
	}
	return deprecated
}

// DeprecatedCredentials returns deprecated credentials (not expired or primary.)
func (r *Rotator) DeprecatedCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.deprecatedCredentials()
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

// RotateWithGrace sets a new primary credential while deprecating a previous credential. The grace
// parameter may be nil to immediately revoke the previous credential.
// It is invalid to specify a grace period when the credential being rotate is a mobile key or
// environment ID.
func (r *Rotator) RotateWithGrace(primary SDKCredential, grace *GracePeriod) {
	switch primary := primary.(type) {
	case config.SDKKey:
		r.updateSDKKey(primary, grace)
	case config.MobileKey:
		if grace != nil {
			panic("programmer error: mobile keys do not support deprecation")
		}
		r.updateMobileKey(primary)
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

func (r *Rotator) updateMobileKey(mobileKey config.MobileKey) {
	if mobileKey == r.MobileKey() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.swapPrimaryMobileKey(mobileKey)
	if previous.Defined() {
		r.expirations = append(r.expirations, previous)
		r.loggers.Infof("Mobile key %s has been immediately revoked", previous.Masked())
	}
}

// MobileGracePeriod represents a grace period within which a particular mobile key is still valid,
// pending revocation. Parallel to GracePeriod for SDK keys.
type MobileGracePeriod struct {
	key    config.MobileKey
	expiry time.Time
	now    time.Time
}

// Expired reports whether the key has already expired.
func (g *MobileGracePeriod) Expired() bool {
	return g.now.After(g.expiry)
}

// NewMobileGracePeriod constructs a new grace period for a mobile key being deprecated. The current
// time must be provided in order to determine if the credential is already expired.
func NewMobileGracePeriod(key config.MobileKey, expiry time.Time, now time.Time) *MobileGracePeriod {
	return &MobileGracePeriod{key, expiry, now}
}

// RotateMobileKeyWithGrace sets a new primary mobile key, optionally deprecating the previous
// primary with a grace period. Pass grace == nil for immediate revocation of the predecessor.
func (r *Rotator) RotateMobileKeyWithGrace(primary config.MobileKey, grace *MobileGracePeriod) {
	r.mu.Lock()
	defer r.mu.Unlock()

	previous := r.swapPrimaryMobileKey(primary)

	if grace == nil {
		if previous.Defined() {
			r.expirations = append(r.expirations, previous)
			r.loggers.Infof("Mobile key %s has been immediately revoked", previous.Masked())
		}
		return
	}

	if previousExpiry, ok := r.deprecatedMobileKeys[grace.key]; ok {
		if previousExpiry != grace.expiry {
			r.loggers.Warnf("Mobile key %s was marked for deprecation with an expiry at %v, but it was previously deprecated with an expiry at %v. The previous expiry will be used.", grace.key.Masked(), grace.expiry, previousExpiry)
		}
		if previous.Defined() {
			r.expirations = append(r.expirations, previous)
			r.loggers.Infof("Mobile key %s has been immediately revoked", previous.Masked())
		}
		return
	}

	if grace.Expired() {
		r.loggers.Infof("Deprecated mobile key %s already expired at %v; ignoring", grace.key.Masked(), grace.expiry)
		return
	}

	r.loggers.Infof("Mobile key %s was marked for deprecation with an expiry at %v", grace.key.Masked(), grace.expiry)
	r.deprecatedMobileKeys[grace.key] = grace.expiry

	if grace.key != previous {
		r.loggers.Infof("Deprecated mobile key %s was not previously managed by Relay", grace.key.Masked())
		r.additions = append(r.additions, grace.key)
	}
}

func (r *Rotator) swapPrimaryMobileKey(newKey config.MobileKey) config.MobileKey {
	if newKey == r.primaryMobileKey {
		return ""
	}
	previous := r.primaryMobileKey
	r.primaryMobileKey = newKey
	r.additions = append(r.additions, newKey)
	if previous.Defined() {
		r.loggers.Infof("Mobile key %s was rotated, new primary mobile key is %s", previous.Masked(), newKey.Masked())
	} else {
		r.loggers.Infof("New primary mobile key is %s", newKey.Masked())
	}
	return previous
}

// ActiveMobileKeys returns the primary mobile key plus all additional non-deprecated mobile keys.
func (r *Rotator) ActiveMobileKeys() []config.MobileKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]config.MobileKey, 0, 1+len(r.additionalMobileKeys))
	if r.primaryMobileKey.Defined() {
		keys = append(keys, r.primaryMobileKey)
	}
	for k := range r.additionalMobileKeys {
		keys = append(keys, k)
	}
	return keys
}

// SetAdditionalMobileKeys synchronizes the set of concurrent mobile keys for this environment.
// Semantics mirror SetAdditionalSDKKeys: keys new to active/expiring are queued as additions;
// transitions between active and expiring leave the key mapped; updated ExpiresAt timestamps are
// accepted as authoritative; omitted keys are revoked immediately without grace.
//
// The primary mobile key is filtered out of the additional set defensively. This method does not
// touch deprecatedMobileKeys, which is owned by the RotateMobileKeyWithGrace path.
func (r *Rotator) SetAdditionalMobileKeys(active []config.MobileKey, expiring map[config.MobileKey]time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	nextActive := make(map[config.MobileKey]struct{}, len(active))
	for _, k := range active {
		if !k.Defined() || k == r.primaryMobileKey {
			continue
		}
		nextActive[k] = struct{}{}
	}
	nextExpiring := make(map[config.MobileKey]time.Time, len(expiring))
	for k, t := range expiring {
		if !k.Defined() || k == r.primaryMobileKey {
			continue
		}
		delete(nextActive, k)
		nextExpiring[k] = t
	}

	current := make(map[config.MobileKey]struct{})
	for k := range r.additionalMobileKeys {
		current[k] = struct{}{}
	}
	for k := range r.expiringAdditionalMobileKeys {
		current[k] = struct{}{}
	}

	for k := range nextActive {
		_, wasAdditional := r.additionalMobileKeys[k]
		_, wasExpiring := r.expiringAdditionalMobileKeys[k]
		switch {
		case wasAdditional:
		case wasExpiring:
			delete(r.expiringAdditionalMobileKeys, k)
			r.additionalMobileKeys[k] = struct{}{}
		default:
			r.additionalMobileKeys[k] = struct{}{}
			r.additions = append(r.additions, k)
			r.loggers.Infof("Additional mobile key %s is now active", k.Masked())
		}
		delete(current, k)
	}

	for k, t := range nextExpiring {
		_, wasAdditional := r.additionalMobileKeys[k]
		previousExpiry, wasExpiring := r.expiringAdditionalMobileKeys[k]
		switch {
		case wasAdditional:
			delete(r.additionalMobileKeys, k)
			r.expiringAdditionalMobileKeys[k] = t
			r.loggers.Infof("Additional mobile key %s is now expiring at %v", k.Masked(), t)
		case wasExpiring:
			if previousExpiry != t {
				r.loggers.Infof("Additional mobile key %s expiry updated from %v to %v", k.Masked(), previousExpiry, t)
				r.expiringAdditionalMobileKeys[k] = t
			}
		default:
			r.expiringAdditionalMobileKeys[k] = t
			r.additions = append(r.additions, k)
			r.loggers.Infof("Additional mobile key %s registered with expiry %v", k.Masked(), t)
		}
		delete(current, k)
	}

	for k := range current {
		delete(r.additionalMobileKeys, k)
		delete(r.expiringAdditionalMobileKeys, k)
		r.expirations = append(r.expirations, k)
		r.loggers.Infof("Additional mobile key %s has been revoked", k.Masked())
	}
}

func (r *Rotator) swapPrimaryKey(newKey config.SDKKey) config.SDKKey {
	if newKey == r.primarySdkKey {
		// There's no swap to be done, we already are using this as primary.
		return ""
	}
	previous := r.primarySdkKey
	r.primarySdkKey = newKey
	r.additions = append(r.additions, newKey)
	r.loggers.Infof("New primary SDK key is %s", newKey.Masked())

	return previous
}

func (r *Rotator) immediatelyRevoke(key config.SDKKey) {
	if key.Defined() {
		r.expirations = append(r.expirations, key)
		r.loggers.Infof("SDK key %s has been immediately revoked", key.Masked())
	}
}

func (r *Rotator) updateSDKKey(sdkKey config.SDKKey, grace *GracePeriod) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Previous will only be .Defined() if there was a previous primary key.
	previous := r.swapPrimaryKey(sdkKey)

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
		r.loggers.Infof("Deprecated SDK key %s already expired at %v; ignoring", grace.key.Masked(), grace.expiry)
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
	r.expirations = append(r.expirations, sdkKey)
}

// StepTime provides the current time to the Rotator, allowing it to compute the set of additions and expirations
// for the tracked credentials since the last time this method was called.
func (r *Rotator) StepTime(now time.Time) (additions []SDKCredential, expirations []SDKCredential) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key, expiry := range r.deprecatedSdkKeys {
		if now.After(expiry) {
			r.expireSDKKey(key)
		}
	}
	for key, expiry := range r.expiringAdditionalSdkKeys {
		if now.After(expiry) {
			r.loggers.Infof("Additional SDK key %s has expired and is no longer valid for authentication", key.Masked())
			delete(r.expiringAdditionalSdkKeys, key)
			r.expirations = append(r.expirations, key)
		}
	}
	for key, expiry := range r.deprecatedMobileKeys {
		if now.After(expiry) {
			r.loggers.Infof("Deprecated mobile key %s has expired and is no longer valid for authentication", key.Masked())
			delete(r.deprecatedMobileKeys, key)
			r.expirations = append(r.expirations, key)
		}
	}
	for key, expiry := range r.expiringAdditionalMobileKeys {
		if now.After(expiry) {
			r.loggers.Infof("Additional mobile key %s has expired and is no longer valid for authentication", key.Masked())
			delete(r.expiringAdditionalMobileKeys, key)
			r.expirations = append(r.expirations, key)
		}
	}

	additions, expirations = r.additions, r.expirations
	r.additions = nil
	r.expirations = nil
	return
}
