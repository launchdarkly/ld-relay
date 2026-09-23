package credential

import (
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
)

// This file holds the single-key rotation API that predates the accepted-set model: one primary SDK
// key plus at most one deprecated key in a grace period. It is retained so that
// EnvContext.UpdateCredential keeps working while the accepted-set model lands, and it is expressed
// entirely in terms of the accepted-set fields so that the rotator has one source of truth.
//
// Delete this file, and EnvContext.UpdateCredential with it, once ReconcileCredentials replaces that
// path. Nothing here is used by Reconcile.

// PrimaryCredentials returns the anchor SDK key, the primary mobile key and the environment ID,
// omitting any that are undefined. It excludes keys that are accepted but carry an expiry.
func (r *Rotator) PrimaryCredentials() []SDKCredential {
	r.mu.RLock()
	defer r.mu.RUnlock()

	creds := make([]SDKCredential, 0, 3)
	for _, cred := range []SDKCredential{r.anchorKey, r.primaryMobileKey, r.primaryEnvironmentID} {
		if cred.Defined() {
			creds = append(creds, cred)
		}
	}
	return creds
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
		r.logger.Info("environment ID was rotated", "previous", previous, "new", envID)
		r.expirations = append(r.expirations, previous)
	} else {
		r.logger.Info("new environment ID", "envID", envID)
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
	r.acceptedMobileKeys[mobileKey] = AcceptedKey{}
	r.additions = append(r.additions, mobileKey)
	if previous.Defined() {
		// A mobile key has no grace period, so the previous one stops authenticating immediately.
		delete(r.acceptedMobileKeys, previous)
		r.expirations = append(r.expirations, previous)
		r.logger.Info("mobile key was rotated", "previous", previous.Masked(), "new", mobileKey.Masked())
	} else {
		r.logger.Info("new primary mobile key", "key", mobileKey.Masked())
	}
}

func (r *Rotator) swapPrimaryKey(newKey config.SDKKey) config.SDKKey {
	if newKey == r.anchorKey {
		// There's no swap to be done, we already are using this as primary.
		return ""
	}
	previous := r.anchorKey
	r.anchorKey = newKey
	// The anchor is always accepted and always permanent.
	r.acceptedSDKKeys[newKey] = AcceptedKey{}
	r.additions = append(r.additions, newKey)
	r.logger.Info("new primary SDK key", "key", newKey.Masked())

	return previous
}

func (r *Rotator) immediatelyRevoke(key config.SDKKey) {
	if key.Defined() {
		delete(r.acceptedSDKKeys, key)
		r.expirations = append(r.expirations, key)
		r.logger.Info("SDK key has been immediately revoked", "key", key.Masked())
	}
}

// deprecatedExpiry returns the expiry recorded for an accepted SDK key that is in a grace period.
// The anchor is never in a grace period, so it never matches.
func (r *Rotator) deprecatedExpiry(key config.SDKKey) (time.Time, bool) {
	info, ok := r.acceptedSDKKeys[key]
	if !ok || info.Expiry == nil || key == r.anchorKey {
		return time.Time{}, false
	}
	return *info.Expiry, true
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

	if previousExpiry, ok := r.deprecatedExpiry(grace.key); ok {
		if previousExpiry != grace.expiry {
			r.logger.Warn("SDK key deprecation expiry mismatch; using previous expiry", "key", grace.key.Masked(), "newExpiry", grace.expiry, "previousExpiry", previousExpiry)
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
		r.logger.Info("deprecated SDK key already expired; ignoring", "key", grace.key.Masked(), "expiry", grace.expiry)
		// The key is not accepted, so a previous anchor that is not the grace key must still be revoked.
		if grace.key != previous {
			r.immediatelyRevoke(previous)
		}
		return
	}

	r.logger.Info("SDK key marked for deprecation", "key", grace.key.Masked(), "expiry", grace.expiry)
	expiry := grace.expiry
	r.acceptedSDKKeys[grace.key] = AcceptedKey{Expiry: &expiry}

	if grace.key != previous {
		r.logger.Info("deprecated SDK key was not previously managed by relay", "key", grace.key.Masked())
		r.additions = append(r.additions, grace.key)
	}
}
