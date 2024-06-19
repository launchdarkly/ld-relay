package credential

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"slices"
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

	// There is only one environment ID active at a given time, and it won't actually be rotated. The mechanism is
	// here to allow setting it in a deferred manner.
	primaryEnvironmentID config.EnvironmentID

	// There can be multiple SDK keys active at a given time, but only one is primary.
	primarySdkKey config.SDKKey

	// Deprecated keys are stored in a map with a started timer for each key representing the deprecation period.
	// Upon expiration, they are removed.
	deprecatedSdkKeys map[config.SDKKey]*deprecatedKey

	expirations chan SDKCredential
	additions   chan SDKCredential
	now         func() time.Time

	mu sync.RWMutex
}

type InitialCredentials struct {
	SDKKey        config.SDKKey
	MobileKey     config.MobileKey
	EnvironmentID config.EnvironmentID
}

func NewRotator(loggers ldlog.Loggers, now func() time.Time) *Rotator {
	r := &Rotator{
		loggers:           loggers,
		deprecatedSdkKeys: make(map[config.SDKKey]*deprecatedKey),
		expirations:       make(chan SDKCredential, 1),
		additions:         make(chan SDKCredential, 1),
		now:               now,
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r
}

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

func (r *Rotator) EnvironmentID() config.EnvironmentID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.primaryEnvironmentID

}

func (r *Rotator) PrimaryCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.primaryCredentials()
}

func (r *Rotator) primaryCredentials() []SDKCredential {
	return slices.DeleteFunc([]SDKCredential{
		r.primarySdkKey,
		r.primaryMobileKey,
		r.primaryEnvironmentID,
	}, func(cred SDKCredential) bool {
		return !cred.Defined()
	})
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

func (r *Rotator) RotateEnvironmentID(envID config.EnvironmentID) {
	if envID == r.EnvironmentID() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.primaryEnvironmentID
	r.primaryEnvironmentID = envID
	r.additions <- envID
	if previous.Defined() {
		r.loggers.Infof("Environment ID %s was rotated, new environment ID is %s", r.primaryEnvironmentID, envID)
		r.expirations <- previous
	} else {
		r.loggers.Infof("New environment ID is %s", envID)
	}
}

func (r *Rotator) RotateMobileKey(mobileKey config.MobileKey) {
	if mobileKey == r.MobileKey() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.primaryMobileKey
	r.primaryMobileKey = mobileKey
	r.additions <- mobileKey
	if previous.Defined() {
		r.expirations <- previous
		r.loggers.Infof("Mobile key %s was rotated, new primary mobile key is %s", r.primaryMobileKey.Masked(), mobileKey.Masked())
	} else {
		r.loggers.Infof("New primary mobile key is %s", mobileKey.Masked())
	}
}

func (r *Rotator) swapPrimaryKey(newKey config.SDKKey) config.SDKKey {
	if newKey == r.SDKKey() {
		// There's no swap to be done, we already are using this as primary.
		return newKey
	}
	previous := r.primarySdkKey
	r.primarySdkKey = newKey
	r.additions <- newKey
	r.loggers.Infof("New primary SDK key is %s", newKey.Masked())

	return previous
}
func (r *Rotator) RotateSDKKey(sdkKey config.SDKKey, deprecation *DeprecationNotice) {
	previous := r.swapPrimaryKey(sdkKey)
	// Immediately revoke the previous SDK key if there's no explicit deprecation notice, otherwise it would
	// hang around forever.
	if previous.Defined() && deprecation == nil {
		r.expirations <- previous
		r.loggers.Infof("SDK key %s has been immediately revoked", previous.Masked())
		return
	}
	if deprecation != nil {
		if prev, ok := r.deprecatedSdkKeys[deprecation.key]; ok {
			r.loggers.Warnf("SDK key %s was marked for deprecation with an expiry at %v, but it was previously deprecated with an expiry at %v. The previous expiry will be used. ", deprecation.key.Masked(), deprecation.expiry, prev.expiry)
			return
		}

		r.loggers.Infof("SDK key %s was marked for deprecation with an expiry at %v", deprecation.key.Masked(), deprecation.expiry)
		r.deprecatedSdkKeys[deprecation.key] = &deprecatedKey{
			expiry: deprecation.expiry,
			timer: time.AfterFunc(deprecation.expiry.Sub(r.now()), func() {
				r.expireSDKKey(deprecation.key)
			})}

		if deprecation.key != previous {
			r.loggers.Infof("Deprecated SDK key %s was not previously managed by Relay", deprecation.key.Masked())
			r.additions <- deprecation.key
		}
	}
}

func (r *Rotator) expireSDKKey(sdkKey config.SDKKey) {
	r.loggers.Infof("Deprecated SDK key %s has expired and is no longer valid for authentication", sdkKey.Masked())
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.deprecatedSdkKeys, sdkKey)
	r.expirations <- sdkKey
}
