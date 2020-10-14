package autoconfig

const (
	logMsgStreamConnecting    = "Connecting to auto-configuration stream at %s"
	logMsgStreamHTTPError     = "HTTP error %d on auto-configuration stream"
	logMsgStreamOtherError    = "Unexpected error on auto-configuration stream: %s"
	logMsgBadKey              = "Invalid auto-configuration key; cannot get environments"
	logMsgDeliberateReconnect = "Will restart auto-configuration stream to get new data due to a policy change"
	logMsgPutEvent            = "Received configuration for %d environment(s)"
	logMsgAddEnv              = "Added environment %s (%s)"
	logMsgUpdateEnv           = "Properties have changed for environment %s (%s)"
	logMsgUpdateBadVersion    = "Ignoring out-of-order update for environment %s (%s)"
	logMsgDeleteEnv           = "Removed environment %s (%s)"
	logMsgDeleteBadVersion    = "Ignoring out-of-order delete for environment %s (%s)"
	logMsgKeyWillExpire       = "Old SDK key ending in %s for environment %s (%s) will expire at %s"
	logMsgKeyExpired          = "Old SDK key ending in %s for environment %s (%s) has expired"
	logMsgEnvHasWrongID       = "Ignoring environment data whose envId %q did not match key %q"
	logMsgUnknownEvent        = "Ignoring unrecognized stream event: %q"
	logMsgWrongPath           = "Ignoring %q event for unknown path %q"
	logMsgMalformedData       = "Received streaming %q event with malformed JSON data (%s); will restart stream"
)
