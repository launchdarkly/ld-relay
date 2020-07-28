package sdks

import (
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
)

// LDClientContext defines a minimal interface for a LaunchDarkly client.
//
// Once the SDK client has been created, Relay does not need to use most of the SDK API. This
// interface provides access to only the necessary methods. This also makes testing simpler, since
// the test code needs to mock only this interface.
type LDClientContext interface {
	Initialized() bool
	SecureModeHash(lduser.User) string
}

// ClientFactoryFunc is a function that creates the LaunchDarkly client. This is normally
// DefaultClientFactory, but it can be changed in order to make configuration changes or for testing.
type ClientFactoryFunc func(sdkKey config.SDKKey, config ld.Config) (LDClientContext, error)

// DefaultClientFactory is the default ClientFactoryFunc implementation, which just passes the
// specified configuration to the SDK client constructor.
func DefaultClientFactory(sdkKey config.SDKKey, config ld.Config) (LDClientContext, error) {
	return ld.MakeCustomClient(string(sdkKey), config, time.Second*10)
}
