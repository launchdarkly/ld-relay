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
}

func NewRotator(loggers ldlog.Loggers) *Rotator {
	r := &Rotator{
		loggers:     loggers,
		timers:      make(map[SDKCredential]*deprecatedCred),
		expirations: make(chan SDKCredential),
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

func (r *Rotator) Deprecate(cred SDKCredential, expiry time.Time) {
	if existing, ok := r.timers[cred]; ok {
		r.loggers.Warnf("Credential %s was marked for deprececation with an expiry time of %v, but it previously expired at %v", cred.Masked(), expiry, existing.expiry)
		return
	}
	r.loggers.Infof("Credential %s has been marked for deprecation with an expiry time of %v", cred.Masked(), expiry)
	state := &deprecatedCred{expired: false}
	state.timer = time.AfterFunc(expiry.Sub(time.Now()), func() {
		r.loggers.Info("Credential %s has expired", cred.Masked())
		r.expirations <- cred
		state.expired = true
	})
	r.timers[cred] = state
}
