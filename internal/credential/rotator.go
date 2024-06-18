package credential

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"sync"
	"time"
)

type DeprecationNotice struct {
	key    config.SDKKey
	expiry time.Time
}

func NewDeprecationNotice(key config.SDKKey, expiry time.Time) *DeprecationNotice {
	return &DeprecationNotice{key: key, expiry: expiry}
}

type deprecatedKey struct {
	expiry time.Time
	timer  *time.Timer
}

type Rotator struct {
	loggers ldlog.Loggers

	// There is only one mobile key active at a given time; it does not support a deprecation period.
	primaryMobileKey config.MobileKey

	// There can be multiple SDK keys active at a given time, but only one is primary.
	primarySdkKey config.SDKKey

	envID config.EnvironmentID

	// Deprecated keys are stored in a map with a started timer for each key representing the deprecation period.
	// Upon expiration, they are removed.
	deprecatedSdkKeys map[config.SDKKey]*deprecatedKey

	expirations chan SDKCredential
	additions   chan SDKCredential
	now         func() time.Time

	mu sync.RWMutex
}

func NewRotator(loggers ldlog.Loggers, envID config.EnvironmentID, now func() time.Time) *Rotator {
	r := &Rotator{
		loggers:           loggers,
		deprecatedSdkKeys: make(map[config.SDKKey]*deprecatedKey),
		envID:             envID,
		expirations:       make(chan SDKCredential),
		additions:         make(chan SDKCredential),
		now:               now,
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r
}

func (r *Rotator) Expirations() <-chan SDKCredential {
	return r.expirations
}

func (r *Rotator) Additions() <-chan SDKCredential {
	return r.additions
}

func (r *Rotator) MobileKey() config.MobileKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.primaryMobileKey
}

func (r *Rotator) SDKKey() config.SDKKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.primarySdkKey
}

func (r *Rotator) PrimaryCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.primaryCredentials()
}

func (r *Rotator) primaryCredentials() []SDKCredential {
	return []SDKCredential{
		r.primarySdkKey,
		r.primaryMobileKey,
		r.envID,
	}
}

func (r *Rotator) deprecatedCredentials() []SDKCredential {
	var deprecated []SDKCredential
	for key := range r.deprecatedSdkKeys {
		deprecated = append(deprecated, key)
	}
	return deprecated
}

func (r *Rotator) DeprecatedCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.deprecatedCredentials()
}

func (r *Rotator) AllCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append(r.primaryCredentials(), r.deprecatedCredentials()...)
}

func (r *Rotator) RotateMobileKey(mobileKey config.MobileKey) {
	if mobileKey == r.MobileKey() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loggers.Infof("Mobile key %s was rotated, new primary mobile key is %s", r.primaryMobileKey.Masked(), mobileKey.Masked())
	previous := r.primaryMobileKey
	r.primaryMobileKey = mobileKey
	r.expirations <- previous
}

func (r *Rotator) RotateSDKKey(sdkKey config.SDKKey, deprecation *DeprecationNotice) {
	// An SDK key can arrive with an optional deprecation notice for a previous key.
	// If there's no deprecation notice, this is an immediate rotation: the new key is the primary key, the old
	// one is removed.
	// If there is a deprecation notice, we need to move the old key to the deprecated state and start a timer.
	// Some gotchas because of the design of the data model:
	// (1) It's possible to receive a notice that names an SDK key that is not the current primary key.
	//    It could be a key that was already deprecated, a key that was expired, or some key we've never seen.
	//    Since we need to make a decision on how to handle it, it shall be:
	//      - If already deprecated (meaning it has a timer), ignore it and log a warning.
	//      - If unseen/expired (can't distinguish since we don't retain it), ignore it and log a warning.
	if sdkKey == r.SDKKey() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if deprecation == nil {
		r.loggers.Infof("SDK key %s was rotated, new primary SDK key is %s", r.primarySdkKey.Masked(), sdkKey.Masked())
		previous := r.primarySdkKey
		r.primarySdkKey = sdkKey
		r.expirations <- previous
		return
	}
	if old, ok := r.deprecatedSdkKeys[deprecation.key]; ok {
		r.loggers.Warnf("SDK key %s was marked for deprecation with an expiry at %v, but it was previously deprecated with an expiry at %v. The previous expiry will be used. ", deprecation.key.Masked(), deprecation.expiry, old.expiry)
		return
	}
	if deprecation.key == r.primarySdkKey {
		r.loggers.Infof("SDK key %s was rotated with an expiry at %v, new primary SDK key is %s", r.primarySdkKey.Masked(), deprecation.expiry, sdkKey.Masked())
		r.primarySdkKey = sdkKey
		r.deprecatedSdkKeys[deprecation.key] = &deprecatedKey{
			expiry: deprecation.expiry,
			timer: time.AfterFunc(deprecation.expiry.Sub(r.now()), func() {
				r.expireSDKKey(deprecation.key)
			})}
		return
	}
	r.loggers.Warnf("SDK key %s was marked for deprecation with an expiry at %v, but this key is not recognized by Relay. It may have already expired; ignoring.", deprecation.key.Masked(), deprecation.expiry)
}

func (r *Rotator) expireSDKKey(sdkKey config.SDKKey) {
	r.loggers.Infof("Deprecated SDK key %s has expired and is no longer valid for authentication", sdkKey.Masked())
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.deprecatedSdkKeys, sdkKey)
	r.expirations <- sdkKey
}
