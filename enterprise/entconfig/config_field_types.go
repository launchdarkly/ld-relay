package entconfig

// AutoConfigKey is a type tag to indicate when a string is used as an auto-config key.
type AutoConfigKey string

// GetAuthorizationHeaderValue for AutoConfigKey returns the same string, since these keys are passed in
// the Authorization header. Note that unlike the other kinds of authorization keys, this one is never
// present in an incoming request; it is only used in requests from Relay to LaunchDarkly.
func (k AutoConfigKey) GetAuthorizationHeaderValue() string {
	return string(k)
}

// UnmarshalText allows the AutoConfigKey type to be set from environment variables.
func (k *AutoConfigKey) UnmarshalText(data []byte) error {
	*k = AutoConfigKey(string(data))
	return nil
}
