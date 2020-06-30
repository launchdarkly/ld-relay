// Package ldconsul provides a Consul-backed persistent data store for the LaunchDarkly Go SDK.
//
// For more details about how and why you can use a persistent data store, see:
// https://docs.launchdarkly.com/v2.0/docs/using-a-persistent-feature-store
//
// To use the Consul data store with the LaunchDarkly client:
//
//     config := ld.Config{
//         DataStore: ldcomponents.PersistentDataStore(ldconsul.DataStore()),
//     }
//     client, err := ld.MakeCustomClient("sdk-key", config, 5*time.Second)
//
// The default Consul configuration uses an address of localhost:8500. You may customize the
// configuration by using the methods of the ldconsul.DataStoreBuilder returned by
// ldconsul.DataStore(). For example:
//
//     config := ld.Config{
//         DataStore: ldcomponents.PersistentDataStore(
//             ldconsul.DataStore().URL(myRedisURL),
//         ).CacheSeconds(30),
//     }
//
// Note that CacheSeconds() is not a method of ldconsul.DataStoreBuilder, but rather a method of
// ldcomponents.PersistentDataStore(), because the caching behavior is provided by the SDK for
// all database integrations.
//
// If you are also using Consul for other purposes, the data store can coexist with
// other data as long as you are not using the same keys. By default, the keys used by the
// data store will always start with "launchdarkly/"; you can change this to another
// prefix if desired.
package ldconsul
