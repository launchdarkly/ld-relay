package credential

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"time"
)

type deprecatedCred struct {
	timer   *time.Timer
	expiry  time.Time
	expired bool
}

type Rotator struct {
	loggers     ldlog.Loggers
	timers      map[SDKCredential]*deprecatedCred
	expirations chan SDKCredential
	now         func() time.Time
}

func NewRotator(loggers ldlog.Loggers, now func() time.Time) *Rotator {
	r := &Rotator{
		loggers:     loggers,
		timers:      make(map[SDKCredential]*deprecatedCred),
		expirations: make(chan SDKCredential),
		now:         now,
	}
	return r
}

func (r *Rotator) Expirations() <-chan SDKCredential {
	return r.expirations
}

func (r *Rotator) Deprecated(cred SDKCredential) bool {
	_, ok := r.timers[cred]
	return ok
}

func (r *Rotator) Expired(cred SDKCredential) bool {
	if state, ok := r.timers[cred]; ok {
		return state.expired
	}
	return false
}

func (r *Rotator) Stop() {
	for _, state := range r.timers {
		state.timer.Stop()
	}
}

func (r *Rotator) Deprecate(cred SDKCredential, expiry time.Time) bool {
	if existing, ok := r.timers[cred]; ok {
		r.loggers.Warnf("Credential %s was marked for deprecation with an expiry time of %v, but it previously expired at %v", cred.Masked(), expiry, existing.expiry)
		return false
	}
	r.loggers.Infof("Credential %s has been marked for deprecation with an expiry time of %v", cred.Masked(), expiry)
	state := &deprecatedCred{expired: false}
	state.timer = time.AfterFunc(expiry.Sub(r.now()), func() {
		r.loggers.Info("Credential %s has expired", cred.Masked())
		state.expired = true
		r.expirations <- cred
	})
	r.timers[cred] = state
	return true
}
