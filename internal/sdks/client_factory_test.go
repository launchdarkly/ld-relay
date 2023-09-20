package sdks

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v7/internal/sharedtest"

	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientFactoryFromLDClientFactory(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		assert.Nil(t, ClientFactoryFromLDClientFactory(nil))
	})

	t.Run("success", func(t *testing.T) {
		factory := ClientFactoryFromLDClientFactory(func(sdkKey string, config ld.Config, timeout time.Duration) (*ld.LDClient, error) {
			config.Offline = true
			return ld.MakeCustomClient(string(sdkKey), config, timeout)
		})
		require.NotNil(t, factory)
		client, err := factory("sdk-key", ld.Config{Logging: ldcomponents.NoLogging()}, 0)
		require.NoError(t, err)
		require.NotNil(t, client)
		defer client.Close()
		assert.Equal(t, interfaces.DataSourceStateValid, client.GetDataSourceStatus().State) // initializes immediately because it's offline
	})

	t.Run("fatal initialization error, client is not returned", func(t *testing.T) {
		// For fatal errors like an invalid configuration, the SDK does not return a client at all
		factory := ClientFactoryFromLDClientFactory(func(sdkKey string, config ld.Config, timeout time.Duration) (*ld.LDClient, error) {
			config.HTTP = makeInvalidHTTPConfiguration()
			return ld.MakeCustomClient(string(sdkKey), config, timeout)
		})
		require.NotNil(t, factory)
		client, err := factory("sdk-key", ld.Config{Logging: ldcomponents.NoLogging()}, 0)
		assert.Nil(t, client)
		assert.Error(t, err)
	})

	t.Run("timeout error, client is returned", func(t *testing.T) {
		// For initialization timeout errors, the SDK *does* return a client along with the error -
		// the test verifies that our wrapper logic preserves this
		factory := ClientFactoryFromLDClientFactory(func(sdkKey string, config ld.Config, timeout time.Duration) (*ld.LDClient, error) {
			config.DataSource = sharedtest.ExistingInstance(sharedtest.DataSourceThatNeverStarts())
			return ld.MakeCustomClient(string(sdkKey), config, timeout)
		})
		require.NotNil(t, factory)
		client, err := factory("sdk-key", ld.Config{Logging: ldcomponents.NoLogging()}, time.Millisecond)
		assert.NotNil(t, client)
		assert.Error(t, err)
	})

	t.Run("nonspecific initialization failure, client is returned", func(t *testing.T) {
		// For conditions where the data source did not successfully start but the configuration was valid, the
		// SDK *does* return a client along with the error - the test verifies that our wrapper logic preserves this
		factory := ClientFactoryFromLDClientFactory(func(sdkKey string, config ld.Config, timeout time.Duration) (*ld.LDClient, error) {
			config.DataSource = sharedtest.ExistingInstance(sharedtest.DataSourceThatStartsWithoutInitializing())
			return ld.MakeCustomClient(string(sdkKey), config, timeout)
		})
		require.NotNil(t, factory)
		client, err := factory("sdk-key", ld.Config{Logging: ldcomponents.NoLogging()}, time.Millisecond)
		assert.NotNil(t, client)
		assert.Error(t, err)
	})
}

func TestDefaultClientFactory(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		config := ld.Config{Offline: true, Logging: ldcomponents.NoLogging()}
		client, err := DefaultClientFactory()("sdk-key", config, time.Second)
		require.NoError(t, err)
		require.NotNil(t, client)
		defer client.Close()
		assert.Equal(t, interfaces.DataSourceStateValid, client.GetDataSourceStatus().State) // initializes immediately because it's offline
	})

	t.Run("initialization error, client is not returned", func(t *testing.T) {
		// See TestClientFactoryFromLDClientFactory for the rationale for this test
		config := ld.Config{HTTP: makeInvalidHTTPConfiguration(), Logging: ldcomponents.NoLogging()}
		client, err := DefaultClientFactory()("sdk-key", config, time.Second)
		assert.Error(t, err)
		assert.Nil(t, client)
	})

	t.Run("nonspecific initialization failure, client is returned", func(t *testing.T) {
		// See TestClientFactoryFromLDClientFactory for the rationale for this test.
		// We can't test the timeout case because currently the timeout is hard-coded to 10 seconds.
		config := ld.Config{
			DataSource: sharedtest.ExistingInstance(sharedtest.DataSourceThatStartsWithoutInitializing()),
			Logging:    ldcomponents.NoLogging(),
		}
		client, err := DefaultClientFactory()("sdk-key", config, time.Second)
		assert.Error(t, err)
		assert.NotNil(t, client)
	})
}

func makeInvalidHTTPConfiguration() *ldcomponents.HTTPConfigurationBuilder {
	// Just do something invalid enough to make SDK client creation fail
	return ldcomponents.HTTPConfiguration().ProxyURL(":::")
}

func TestDataStoreStatusTracking(t *testing.T) {
	startTime := time.Now()

	store := &fakeStore{}
	config := ld.Config{Offline: true, DataStore: fakeStoreFactory{store}, Logging: ldcomponents.NoLogging()}
	client, err := DefaultClientFactory()("sdk-key", config, 0)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer client.Close()

	status1 := client.GetDataStoreStatus()
	assert.True(t, status1.Available)
	assert.False(t, status1.LastUpdated.Before(startTime))

	time.Sleep(time.Millisecond)

	store.updates.UpdateStatus(interfaces.DataStoreStatus{Available: false})

	time.Sleep(time.Millisecond * 100) // there's no good way to synchronize on this update
	status2 := client.GetDataStoreStatus()
	assert.False(t, status2.Available)
	assert.True(t, status1.LastUpdated.Before(status2.LastUpdated))
}

type fakeStore struct {
	updates subsystems.DataStoreUpdateSink
}

type fakeStoreFactory struct {
	instance *fakeStore
}

func (f fakeStoreFactory) Build(ctx subsystems.ClientContext) (subsystems.DataStore, error) {
	f.instance.updates = ctx.GetDataStoreUpdateSink()
	return f.instance, nil
}

func (f *fakeStore) Close() error { return nil }

func (f *fakeStore) Init(allData []ldstoretypes.Collection) error { return nil }

func (f *fakeStore) Get(kind ldstoretypes.DataKind, key string) (ldstoretypes.ItemDescriptor, error) {
	return ldstoretypes.ItemDescriptor{}, nil
}

func (f *fakeStore) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	return nil, nil
}

func (f *fakeStore) Upsert(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) (bool, error) {
	return false, nil
}

func (f *fakeStore) IsInitialized() bool { return true }

func (f *fakeStore) IsStatusMonitoringEnabled() bool { return true }
