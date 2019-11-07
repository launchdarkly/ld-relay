// Package ldconsul provides a Consul-backed feature store for the LaunchDarkly Go SDK.
//
// For more details about how and why you can use a persistent feature store, see:
// https://docs.launchdarkly.com/v2.0/docs/using-a-persistent-feature-store
//
// To use the Consul feature store with the LaunchDarkly client:
//
//     factory, err := ldconsul.NewConsulFeatureStoreFactory()
//     if err != nil { ... }
//
//     config := ld.DefaultConfig
//     config.FeatureStoreFactory = factory
//     client, err := ld.MakeCustomClient("sdk-key", config, 5*time.Second)
//
// The default Consul configuration uses an address of localhost:8500. To customize any
// properties of Consul, you can use the Address() or Config() functions.
//
// If you are also using Consul for other purposes, the feature store can coexist with
// other data as long as you are not using the same keys. By default, the keys used by the
// feature store will always start with "launchdarkly/"; you can change this to another
// prefix if desired.
package ldconsul

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	c "github.com/hashicorp/consul/api"
	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v4/utils"
)

// Implementation notes:
//
// - Feature flags, segments, and any other kind of entity the LaunchDarkly client may wish
// to store, are stored as individual items with the key "{prefix}/features/{flag-key}",
// "{prefix}/segments/{segment-key}", etc.
// - The special key "{prefix}/$inited" indicates that the store contains a complete data set.
// - Since Consul has limited support for transactions (they can't contain more than 64
// operations), the Init method-- which replaces the entire data store-- is not guaranteed to
// be atomic, so there can be a race condition if another process is adding new data via
// Upsert. To minimize this, we don't delete all the data at the start; instead, we update
// the items we've received, and then delete all other items. That could potentially result in
// deleting new data from another process, but that would be the case anyway if the Init
// happened to execute later than the Upsert; we are relying on the fact that normally the
// process that did the Init will also receive the new data shortly and do its own Upsert.

const (
	// DefaultCacheTTL is the amount of time that recently read or updated items will be cached
	// in memory, unless you specify otherwise with the CacheTTL option.
	DefaultCacheTTL = 15 * time.Second
	// DefaultPrefix is a string that is prepended (along with a slash) to all Consul keys used
	// by the feature store. You can change this value with the Prefix() option.
	DefaultPrefix = "launchdarkly"
)

const (
	initedKey = "$inited"
)

type featureStoreOptions struct {
	consulConfig c.Config
	prefix       string
	cacheTTL     time.Duration
	logger       ld.Logger
}

// Internal implementation of the Consul-backed feature store. We don't export this - we just
// return an ld.FeatureStore.
type featureStore struct {
	options    featureStoreOptions
	client     *c.Client
	loggers    ldlog.Loggers
	testTxHook func() // for unit testing of concurrent modifications
}

// FeatureStoreOption is the interface for optional configuration parameters that can be
// passed to NewConsulFeatureStoreFactory. These include UseConfig, Prefix, CacheTTL, and UseLogger.
type FeatureStoreOption interface {
	apply(opts *featureStoreOptions) error
}

type configOption struct {
	config c.Config
}

func (o configOption) apply(opts *featureStoreOptions) error {
	opts.consulConfig = o.config
	return nil
}

// Config creates an option for NewConsulFeatureStoreFactory, to specify an entire configuration
// for the Consul driver. This overwrites any previous Consul settings that may have been
// specified.
//
//     factory, err := ldconsul.NewConsulFeatureStoreFactory(ldconsul.Config(myConsulConfig))
func Config(config c.Config) FeatureStoreOption {
	return configOption{config}
}

type addressOption struct {
	address string
}

func (o addressOption) apply(opts *featureStoreOptions) error {
	opts.consulConfig.Address = o.address
	return nil
}

// Address creates an option for NewConsulFeatureStoreFactory, to set the address of the Consul server.
// If placed after Config(), this modifies the previously specified configuration.
//
//     factory, err := ldconsul.NewConsulFeatureStoreFactory(ldconsul.Address("http://consulhost:8100"))
func Address(address string) FeatureStoreOption {
	return addressOption{address}
}

type prefixOption struct {
	prefix string
}

func (o prefixOption) apply(opts *featureStoreOptions) error {
	opts.prefix = o.prefix
	return nil
}

// Prefix creates an option for NewConsulFeatureStoreFactory, to specify a prefix for namespacing
// the feature store's keys. The default value is DefaultPrefix.
//
//     factory, err := ldconsul.NewConsulFeatureStoreFactory(ldconsul.Prefix("ld-data"))
func Prefix(prefix string) FeatureStoreOption {
	return prefixOption{prefix}
}

type cacheTTLOption struct {
	ttl time.Duration
}

func (o cacheTTLOption) apply(opts *featureStoreOptions) error {
	opts.cacheTTL = o.ttl
	return nil
}

// CacheTTL creates an option for NewConsulFeatureStoreFactory, to specify how long flag data should be
// cached in memory to avoid rereading it from Consul.
//
// The default value is DefaultCacheTTL. A value of zero disables in-memory caching completely.
// A negative value means data is cached forever (i.e. it will only be read again from the
// database if the SDK is restarted). Use the "cached forever" mode with caution: it means
// that in a scenario where multiple processes are sharing the database, and the current
// process loses connectivity to LaunchDarkly while other processes are still receiving
// updates and writing them to the database, the current process will have stale data.
//
//     factory, err := ldconsul.NewConsulFeatureStoreFactory(ldconsul.CacheTTL(30*time.Second))
func CacheTTL(ttl time.Duration) FeatureStoreOption {
	return cacheTTLOption{ttl}
}

type loggerOption struct {
	logger ld.Logger
}

func (o loggerOption) apply(opts *featureStoreOptions) error {
	opts.logger = o.logger
	return nil
}

// Logger creates an option for NewConsulFeatureStore, to specify where to send log output.
//
// If you use NewConsulFeatureStoreFactory rather than the deprecated constructor, you normally do
// not need to specify a logger because it will use the same logging configuration as the SDK client.
//
//     store, err := ldconsul.NewConsulFeatureStore(ldconsul.Logger(myLogger))
func Logger(logger ld.Logger) FeatureStoreOption {
	return loggerOption{logger}
}

// NewConsulFeatureStore creates a new Consul-backed feature store with an optional memory cache. You
// may customize its behavior with any number of FeatureStoreOption values, such as Config, Address,
// Prefix, CacheTTL, and Logger.
//
// Deprecated: Please use NewConsulFeatureStoreFactory instead.
func NewConsulFeatureStore(options ...FeatureStoreOption) (ld.FeatureStore, error) {
	factory, err := NewConsulFeatureStoreFactory(options...)
	if err != nil {
		return nil, err
	}
	return factory(ld.Config{})
}

// NewConsulFeatureStoreFactory returns a factory function for a Consul-backed feature store with an
// optional memory cache. You may customize its behavior with any number of FeatureStoreOption values,
// such as Config, Address, Prefix, CacheTTL, and Logger.
//
// Set the FeatureStoreFactory field in your Config to the returned value. Because this is specified
// as a factory function, the Consul client is not actually created until you create the SDK client.
// This also allows it to use the same logging configuration as the SDK, so you do not have to
// specify the Logger option separately.
func NewConsulFeatureStoreFactory(options ...FeatureStoreOption) (ld.FeatureStoreFactory, error) {
	configuredOptions, err := validateOptions(options...)
	if err != nil {
		return nil, err
	}
	return func(ldConfig ld.Config) (ld.FeatureStore, error) {
		store, err := newConsulFeatureStoreInternal(configuredOptions, ldConfig)
		if err != nil {
			return nil, err
		}
		return utils.NewNonAtomicFeatureStoreWrapperWithConfig(store, ldConfig), nil
	}, nil
}

func validateOptions(options ...FeatureStoreOption) (featureStoreOptions, error) {
	ret := featureStoreOptions{
		consulConfig: *c.DefaultConfig(),
		cacheTTL:     DefaultCacheTTL,
	}
	for _, o := range options {
		err := o.apply(&ret)
		if err != nil {
			return ret, err
		}
	}
	return ret, nil
}

func newConsulFeatureStoreInternal(configuredOptions featureStoreOptions, ldConfig ld.Config) (*featureStore, error) {
	store := &featureStore{
		options: configuredOptions,
		loggers: ldConfig.Loggers, // copied by value so we can modify it
	}
	store.loggers.SetBaseLogger(configuredOptions.logger) // has no effect if it is nil
	store.loggers.SetPrefix("ConsulFeatureStore:")

	if store.options.prefix == "" {
		store.options.prefix = DefaultPrefix
	}

	store.loggers.Infof("Using config: %+v", store.options.consulConfig)

	client, err := c.NewClient(&store.options.consulConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to configure Consul client: %s", err)
	}
	store.client = client
	return store, nil
}

func (store *featureStore) GetCacheTTL() time.Duration {
	return store.options.cacheTTL
}

func (store *featureStore) GetInternal(kind ld.VersionedDataKind, key string) (ld.VersionedData, error) {
	item, _, err := store.getEvenIfDeleted(kind, key)
	return item, err
}

func (store *featureStore) GetAllInternal(kind ld.VersionedDataKind) (map[string]ld.VersionedData, error) {
	results := make(map[string]ld.VersionedData)

	kv := store.client.KV()
	pairs, _, err := kv.List(store.featuresKey(kind), nil)

	if err != nil {
		return results, fmt.Errorf("List failed for %s: %s", kind, err)
	}

	for _, pair := range pairs {
		item, jsonErr := utils.UnmarshalItem(kind, pair.Value)

		if jsonErr != nil {
			return nil, fmt.Errorf("unable to unmarshal %s: %s", kind, err)
		}

		results[item.GetKey()] = item
	}
	return results, nil
}

func (store *featureStore) InitCollectionsInternal(allData []utils.StoreCollection) error {
	kv := store.client.KV()

	// Start by reading the existing keys; we will later delete any of these that weren't in allData.
	pairs, _, err := kv.List(store.options.prefix, nil)
	if err != nil {
		return fmt.Errorf("failed to get existing items prior to Init: %s", err)
	}
	oldKeys := make(map[string]bool)
	for _, p := range pairs {
		oldKeys[p.Key] = true
	}

	ops := make([]*c.KVTxnOp, 0)

	for _, coll := range allData {
		for _, item := range coll.Items {
			data, jsonErr := json.Marshal(item)
			if jsonErr != nil {
				return fmt.Errorf("failed to marshal %s key %s: %s", coll.Kind, item.GetKey(), jsonErr)
			}

			key := store.featureKeyFor(coll.Kind, item.GetKey())
			op := &c.KVTxnOp{Verb: c.KVSet, Key: key, Value: data}
			ops = append(ops, op)

			oldKeys[key] = false
		}
	}

	// Now delete any previously existing items whose keys were not in the current data
	for k, v := range oldKeys {
		if v && k != store.initedKey() {
			op := &c.KVTxnOp{Verb: c.KVDelete, Key: k}
			ops = append(ops, op)
		}
	}

	// Add the special key that indicates the store is initialized
	op := &c.KVTxnOp{Verb: c.KVSet, Key: store.initedKey(), Value: []byte{}}
	ops = append(ops, op)

	// Submit all the queued operations, using as many transactions as needed. (We're not really using
	// transactions for atomicity, since we're not atomic anyway if there's more than one transaction,
	// but batching them reduces the number of calls to the server.)
	return batchOperations(kv, ops)
}

func (store *featureStore) UpsertInternal(kind ld.VersionedDataKind, newItem ld.VersionedData) (ld.VersionedData, error) {
	data, jsonErr := json.Marshal(newItem)
	if jsonErr != nil {
		return nil, fmt.Errorf("failed to marshal %s key %s: %s", kind, newItem.GetKey(), jsonErr)
	}
	key := newItem.GetKey()

	// We will potentially keep retrying to store indefinitely until someone's write succeeds
	for {
		// Get the item
		oldItem, modifyIndex, err := store.getEvenIfDeleted(kind, key)

		if err != nil {
			return nil, err
		}

		// Check whether the item is stale. If so, don't do the update (and return the existing item to
		// FeatureStoreWrapper so it can be cached)
		if oldItem != nil && oldItem.GetVersion() >= newItem.GetVersion() {
			return oldItem, nil
		}

		if store.testTxHook != nil { // instrumentation for unit tests
			store.testTxHook()
		}

		// Otherwise, try to write. We will do a compare-and-set operation, so the write will only succeed if
		// the key's ModifyIndex is still equal to the previous value returned by getEvenIfDeleted. If the
		// previous ModifyIndex was zero, it means the key did not previously exist and the write will only
		// succeed if it still doesn't exist.
		kv := store.client.KV()
		p := &c.KVPair{
			Key:         store.featureKeyFor(kind, key),
			ModifyIndex: modifyIndex,
			Value:       data,
		}
		written, _, err := kv.CAS(p, nil)

		if err != nil {
			return nil, err
		}

		if written {
			return newItem, nil // success
		}
		// If we failed, retry the whole shebang
		store.loggers.Debug("Concurrent modification detected, retrying")
	}
}

func (store *featureStore) InitializedInternal() bool {
	kv := store.client.KV()
	pair, _, err := kv.Get(store.initedKey(), nil)
	return pair != nil && err == nil
}

func (store *featureStore) IsStoreAvailable() bool {
	// Using a simple Get query here rather than the Consul Health API, because the latter seems to be
	// oriented toward monitoring of specific nodes or services; what we really want to know is just
	// whether a basic operation can succeed.
	kv := store.client.KV()
	_, _, err := kv.Get(store.initedKey(), nil)
	return err == nil
}

func (store *featureStore) getEvenIfDeleted(kind ld.VersionedDataKind, key string) (retrievedItem ld.VersionedData,
	modifyIndex uint64, err error) {
	var defaultModifyIndex = uint64(0)

	kv := store.client.KV()

	pair, _, err := kv.Get(store.featureKeyFor(kind, key), nil)

	if err != nil || pair == nil {
		return nil, defaultModifyIndex, err
	}

	item, jsonErr := utils.UnmarshalItem(kind, pair.Value)

	if jsonErr != nil {
		return nil, defaultModifyIndex, fmt.Errorf("failed to unmarshal %s key %s: %s", kind, key, jsonErr)
	}

	return item, pair.ModifyIndex, nil
}

func batchOperations(kv *c.KV, ops []*c.KVTxnOp) error {
	for i := 0; i < len(ops); {
		j := i + 64
		if j > len(ops) {
			j = len(ops)
		}
		batch := ops[i:j]
		ok, resp, _, err := kv.Txn(batch, nil)
		if err != nil {
			return err
		}
		if !ok {
			errs := make([]string, 0)
			for _, te := range resp.Errors {
				errs = append(errs, te.What)
			}
			return fmt.Errorf("Consul transaction failed: %s", strings.Join(errs, ", "))
		}
		i = j
	}
	return nil
}

func (store *featureStore) featuresKey(kind ld.VersionedDataKind) string {
	return store.options.prefix + "/" + kind.GetNamespace()
}

func (store *featureStore) featureKeyFor(kind ld.VersionedDataKind, k string) string {
	return store.options.prefix + "/" + kind.GetNamespace() + "/" + k
}

func (store *featureStore) initedKey() string {
	return store.options.prefix + "/" + initedKey
}
