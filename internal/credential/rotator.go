package credential

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"slices"
	"sync"
	"time"
)

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
	deprecatedSdkKeys map[config.SDKKey]time.Time

	expirations []SDKCredential
	additions   []SDKCredential

	mu sync.RWMutex
}

type InitialCredentials struct {
	SDKKey        config.SDKKey
	MobileKey     config.MobileKey
	EnvironmentID config.EnvironmentID
}

func NewRotator(loggers ldlog.Loggers) *Rotator {
	r := &Rotator{
		loggers:           loggers,
		deprecatedSdkKeys: make(map[config.SDKKey]time.Time),
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

func (r *Rotator) Rotate(cred SDKCredential) {
	r.RotateWithGrace(cred, nil)
}

type GracePeriod struct {
	key    config.SDKKey
	expiry time.Time
}

func NewGracePeriod(key config.SDKKey, expiry time.Time) *GracePeriod {
	return &GracePeriod{key, expiry}
}

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
	previous := r.primaryMobileKey
	r.primaryMobileKey = mobileKey
	r.additions = append(r.additions, mobileKey)
	if previous.Defined() {
		r.expirations = append(r.expirations, previous)
		r.loggers.Infof("Mobile key %s was rotated, new primary mobile key is %s", r.primaryMobileKey.Masked(), mobileKey.Masked())
	} else {
		r.loggers.Infof("New primary mobile key is %s", mobileKey.Masked())
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
func (r *Rotator) updateSDKKey(sdkKey config.SDKKey, grace *GracePeriod) {
	r.mu.Lock()
	defer r.mu.Unlock()

	previous := r.swapPrimaryKey(sdkKey)
	// Immediately revoke the previous SDK key if there's no explicit deprecation notice, otherwise it would
	// hang around forever.
	if previous.Defined() && grace == nil {
		r.expirations = append(r.expirations, previous)
		r.loggers.Infof("SDK key %s has been immediately revoked", previous.Masked())
		return
	}
	if grace != nil {
		if previousExpiry, ok := r.deprecatedSdkKeys[grace.key]; ok {
			r.loggers.Warnf("SDK key %s was marked for deprecation with an expiry at %v, but it was previously deprecated with an expiry at %v. The previous expiry will be used. ", grace.key.Masked(), grace.expiry, previousExpiry)
			return
		}

		r.loggers.Infof("SDK key %s was marked for deprecation with an expiry at %v", grace.key.Masked(), grace.expiry)
		r.deprecatedSdkKeys[grace.key] = grace.expiry

		if grace.key != previous {
			r.loggers.Infof("Deprecated SDK key %s was not previously managed by Relay", grace.key.Masked())
			r.additions = append(r.additions, grace.key)
		}
	}
}

func (r *Rotator) expireSDKKey(sdkKey config.SDKKey) {
	r.loggers.Infof("Deprecated SDK key %s has expired and is no longer valid for authentication", sdkKey.Masked())
	delete(r.deprecatedSdkKeys, sdkKey)
	r.expirations = append(r.expirations, sdkKey)
}

func (r *Rotator) Query(now time.Time) (additions []SDKCredential, expirations []SDKCredential) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key, expiry := range r.deprecatedSdkKeys {
		if now.After(expiry) {
			r.expireSDKKey(key)
		}
	}

	additions, expirations = r.additions, r.expirations
	r.additions = nil
	r.expirations = nil
	return
}
