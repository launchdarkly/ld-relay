package sdks

import (
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v7/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	ld "github.com/launchdarkly/go-server-sdk/v6"
	"github.com/launchdarkly/go-server-sdk/v6/interfaces"
)

// LDClientContext defines a minimal interface for a LaunchDarkly client.
//
// Once the SDK client has been created, Relay does not need to use most of the SDK API. This
// interface provides access to only the necessary methods. This also makes testing simpler, since
// the test code needs to mock only this interface.
type LDClientContext interface {
	Initialized() bool
	SecureModeHash(ldcontext.Context) string
	GetDataSourceStatus() interfaces.DataSourceStatus
	GetDataStoreStatus() DataStoreStatusInfo
	Close() error
}

type ldClientContextImpl struct {
	*ld.LDClient
	storeStatusTime time.Time
	lock            sync.Mutex
}

// DataStoreStatusInfo combines the Available property from interfaces.DataStoreStatus with a
// timestamp.
type DataStoreStatusInfo struct {
	// Available is copied from interfaces.DataStoreStatus.
	Available bool

	// LastUpdated is the time when the status last changed.
	LastUpdated time.Time
}

// ClientFactoryFunc is a function that creates the LaunchDarkly client. This is normally
// DefaultClientFactory, but it can be changed in order to make configuration changes or for testing.
type ClientFactoryFunc func(sdkKey config.SDKKey, config ld.Config, timeout time.Duration) (LDClientContext, error)

// LDClientConstructor is the function type of the underlying SDK client constructor.
type LDClientConstructor func(sdkKey string, config ld.Config, timeout time.Duration) (*ld.LDClient, error)

// DefaultClientFactory is the default ClientFactoryFunc implementation, which just passes the
// specified configuration to the SDK client constructor.
func DefaultClientFactory() ClientFactoryFunc {
	return ClientFactoryFromLDClientFactory(ld.MakeCustomClient)
}

// ClientFactoryFromLDClientFactory translates from the client factory type that we expose to host
// applications, which uses the real LDClient type, to the more general factory type that we use
// internally which uses the sdks.ClientFactoryFunc abstraction. The latter makes our code a bit
// cleaner and easier to test, but isn't of any use when hosting Relay in an application.
func ClientFactoryFromLDClientFactory(fn LDClientConstructor) ClientFactoryFunc {
	if fn == nil {
		return nil
	}
	return func(sdkKey config.SDKKey, config ld.Config, timeout time.Duration) (LDClientContext, error) {
		c, err := fn(string(sdkKey), config, timeout)
		if c == nil {
			return nil, err
		}
		return wrapLDClient(c), err
	}
}

func wrapLDClient(c *ld.LDClient) LDClientContext {
	ret := &ldClientContextImpl{LDClient: c}
	ret.storeStatusTime = time.Now()
	// In Relay's status reporting, we want to be provide a "stateSince" timestamp for the data store status
	// like we have for the data source status. However, the SDK API does not provide this by default. So to
	// keep track of the time of the last status change, we add a status listener that just updates the
	// timestamp whenever it gets a new status.
	storeStatusCh := c.GetDataStoreStatusProvider().AddStatusListener()
	go func() {
		for range storeStatusCh { // if the SDK client is closed, this channel will also be closed
			ret.lock.Lock()
			ret.storeStatusTime = time.Now()
			ret.lock.Unlock()
		}
	}()
	return ret
}

func (c *ldClientContextImpl) GetDataSourceStatus() interfaces.DataSourceStatus {
	return c.GetDataSourceStatusProvider().GetStatus()
}

func (c *ldClientContextImpl) GetDataStoreStatus() DataStoreStatusInfo {
	status := c.GetDataStoreStatusProvider().GetStatus()
	c.lock.Lock()
	statusTime := c.storeStatusTime
	c.lock.Unlock()
	return DataStoreStatusInfo{
		Available:   status.Available,
		LastUpdated: statusTime,
	}
}
