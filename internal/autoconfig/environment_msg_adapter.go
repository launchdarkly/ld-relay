package autoconfig

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
)

// EnvironmentMsgAdapter converts the AutoConfig JSON representation of environments (EnvironmentRep) into an internal
// representation expected by MessageHandler. This translation step is necessary because the AutoConfig protocol
// contains extra information that core Relay Proxy logic doesn't need to know about.
type EnvironmentMsgAdapter struct {
	msgHandler MessageHandler
	loggers    ldlog.Loggers
	keyChecker KeyChecker
}

// Sanity check that EnvironmentMsgAdapter implements the ItemReceiver interface.
var _ ItemReceiver[envfactory.EnvironmentRep] = (*EnvironmentMsgAdapter)(nil)

// KeyChecker defines a component that can detect if the representation for an expiring
// SDK key is invalid - that is, if it should be ignored, instead of being forwarded deeper into
// the system. This is necessary because sometimes LaunchDarkly sends ExpiringKeyReps with expiration dates
// in the past, and those shouldn't trigger a key update.
type KeyChecker interface {
	// IgnoreExpiringSDKKey should return true if the ExpiringKeyRep within the given EnvironmentRep should be ignored.
	IgnoreExpiringSDKKey(key envfactory.EnvironmentRep) bool
}

func NewEnvironmentMsgAdapter(
	handler MessageHandler,
	keyChecker KeyChecker,
	loggers ldlog.Loggers) *EnvironmentMsgAdapter {
	return &EnvironmentMsgAdapter{
		msgHandler: handler,
		loggers:    loggers,
		keyChecker: keyChecker,
	}
}

// Insert is responsible for converting the incoming EnvironmentRep into EnvironmentParams, inspecting the
// potentially-expiring SDK key, and then forwarding it to the underlying MessageHandler's AddEnvironment method.
func (e *EnvironmentMsgAdapter) Insert(env envfactory.EnvironmentRep) {
	params := env.ToParams()
	if e.keyChecker.IgnoreExpiringSDKKey(env) {
		params.ExpiringSDKKey = ""
	}
	e.msgHandler.AddEnvironment(params)
}

// Update is responsible for converting the incoming EnvironmentRep into EnvironmentParams, inspecting the
// potentially-expiring SDK key, and then forwarding it to the underling MessageHandler's UpdateEnvironment method.
func (e *EnvironmentMsgAdapter) Update(env envfactory.EnvironmentRep) {
	params := env.ToParams()
	if e.keyChecker.IgnoreExpiringSDKKey(env) {
		params.ExpiringSDKKey = ""
	}
	e.msgHandler.UpdateEnvironment(params)
}

// Delete forwards the ID to the underlying MessageHandler's DeleteEnvironment method.
func (e *EnvironmentMsgAdapter) Delete(id string) {
	e.msgHandler.DeleteEnvironment(config.EnvironmentID(id))
}
