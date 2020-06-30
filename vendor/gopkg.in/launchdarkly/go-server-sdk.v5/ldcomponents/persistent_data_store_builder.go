package ldcomponents

import (
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/internal"
)

// PersistentDataStoreDefaultCacheTime is the default amount of time that recently read or updated items
// will be cached in memory, if you use PersistentDataStore(). You can specify otherwise with the
// PersistentDataStoreBuilder.CacheTime() option.
const PersistentDataStoreDefaultCacheTime = 15 * time.Second

// PersistentDataStore returns a configuration builder for some implementation of a persistent data store.
//
// This method is used in conjunction with another factory object provided by specific components
// such as the Redis integration. The latter provides builder methods for options that are specific
// to that integration, while the PersistentDataStoreBuilder provides options that are
// applicable to any persistent data store (such as caching). For example:
//
//     config := ld.Config{
//         DataStore: ldcomponents.PersistentDataStore(
//             ldredis.DataStore().URL("redis://my-redis-host"),
//         ).CacheSeconds(15),
//     }
//
// See PersistentDataStoreBuilder for more on how this method is used.
//
// For more information on the available persistent data store implementations, see the reference
// guide on "Using a persistent feature store": https://docs.launchdarkly.com/sdk/concepts/feature-store
func PersistentDataStore(persistentDataStoreFactory interfaces.PersistentDataStoreFactory) *PersistentDataStoreBuilder {
	return &PersistentDataStoreBuilder{
		persistentDataStoreFactory: persistentDataStoreFactory,
		cacheTTL:                   PersistentDataStoreDefaultCacheTime,
	}
}

// PersistentDataStoreBuilder is a configurable factory for a persistent data store.
//
// Several database integrations exist for the LaunchDarkly SDK, each with its own behavior and options
// specific to that database; this is described via some implementation of PersistentDataStoreFactory.
// There is also universal behavior that the SDK provides for all persistent data stores, such as caching;
// the PersistentDataStoreBuilder adds this.
//
// After configuring this object, store it in the DataSource field of your SDK configuration. For example,
// using the Redis integration:
//
//     config := ld.Config{
//         DataStore: ldcomponents.PersistentDataStore(
//             ldredis.DataStore().URL("redis://my-redis-host"),
//         ).CacheSeconds(15),
//     }
//
//
// In this example, URL() is an option specifically for the Redis integration, whereas CacheSeconds() is
// an option that can be used for any persistent data store.
type PersistentDataStoreBuilder struct {
	persistentDataStoreFactory interfaces.PersistentDataStoreFactory
	cacheTTL                   time.Duration
}

// CacheTime specifies the cache TTL. Items will be evicted from the cache after this amount of time
// from the time when they were originally cached.
//
// If the value is zero, caching is disabled (equivalent to NoCaching).
//
// If the value is negative, data is cached forever (equivalent to CacheForever).
func (b *PersistentDataStoreBuilder) CacheTime(cacheTime time.Duration) *PersistentDataStoreBuilder {
	b.cacheTTL = cacheTime
	return b
}

// CacheSeconds is a shortcut for calling CacheTime with a duration in seconds.
func (b *PersistentDataStoreBuilder) CacheSeconds(cacheSeconds int) *PersistentDataStoreBuilder {
	return b.CacheTime(time.Duration(cacheSeconds) * time.Second)
}

// CacheForever specifies that the in-memory cache should never expire. In this mode, data will be
// written to both the underlying persistent store and the cache, but will only ever be read from the
// persistent store if the SDK is restarted.
//
// Use this mode with caution: it means that in a scenario where multiple processes are sharing
// the database, and the current process loses connectivity to LaunchDarkly while other processes
// are still receiving updates and writing them to the database, the current process will have
// stale data.
func (b *PersistentDataStoreBuilder) CacheForever() *PersistentDataStoreBuilder {
	return b.CacheTime(-1 * time.Millisecond)
}

// NoCaching specifies that the SDK should not use an in-memory cache for the persistent data store.
// This means that every feature flag evaluation will trigger a data store query.
func (b *PersistentDataStoreBuilder) NoCaching() *PersistentDataStoreBuilder {
	return b.CacheTime(0)
}

// CreateDataStore is called by the SDK to create the data store implemntation object.
func (b *PersistentDataStoreBuilder) CreateDataStore(
	context interfaces.ClientContext,
	dataStoreUpdates interfaces.DataStoreUpdates,
) (interfaces.DataStore, error) {
	core, err := b.persistentDataStoreFactory.CreatePersistentDataStore(context)
	if err != nil {
		return nil, err
	}
	return internal.NewPersistentDataStoreWrapper(core, dataStoreUpdates, b.cacheTTL,
		context.GetLogging().GetLoggers()), nil
}

// DescribeConfiguration is used internally by the SDK to inspect the configuration.
func (b *PersistentDataStoreBuilder) DescribeConfiguration() ldvalue.Value {
	if dd, ok := b.persistentDataStoreFactory.(interfaces.DiagnosticDescription); ok {
		return dd.DescribeConfiguration()
	}
	return ldvalue.String("custom")
}
