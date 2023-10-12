package autoconfig

const (
	logMsgStreamConnecting    = "Connecting to auto-configuration stream (%s)"
	logMsgStreamHTTPError     = "HTTP error %d on auto-configuration stream"
	logMsgBadURL              = "Couldn't construct auto-configuration URL: %v"
	logMsgStreamOtherError    = "Unexpected error on auto-configuration stream: %s"
	logMsgBadKey              = "Invalid auto-configuration key; cannot get environments"
	logMsgDeliberateReconnect = "Will restart auto-configuration stream to get new data due to a policy change"
	logMsgPutEvent            = "Received configuration for %d environment(s)"
	logMsgAddEnv              = "Added %s"
	logMsgUpdateEnv           = "Properties have changed for %s"
	logMsgUpdateBadVersion    = "Ignoring out-of-order update for %s"
	logMsgDeleteEnv           = "Removed %s"
	logMsgDeleteBadVersion    = "Ignoring out-of-order delete for %s"
	logMsgKeyWillExpire       = "Old SDK key ending in %s for %s will expire at %s"
	logMsgKeyExpired          = "Old SDK key ending in %s for environment %s (%s) has expired"
	logMsgEnvHasWrongID       = "Ignoring environment data whose envId %q did not match key %q"
	logMsgUnknownEvent        = "Ignoring unrecognized stream event: %q"
	logMsgWrongPath           = "Ignoring %q event for unknown path %q"
	logMsgMalformedData       = "Received streaming %q event with malformed JSON data (%s); will restart stream"

	logMsgUnknownEntity = "Ignoring unknown entity: %s"
)
