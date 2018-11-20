// Package ldconsul provides a Consul-backed feature store for the LaunchDarkly Go SDK.
//
// For more details about how and why you can use a persistent feature store, see:
// https://docs.launchdarkly.com/v2.0/docs/using-a-persistent-feature-store
//
// To use the Consul feature store with the LaunchDarkly client:
//
//     store, err := ldconsul.NewConsulFeatureStore()
//     if err != nil { ... }
//
//     config := ld.DefaultConfig
//     config.FeatureStore = store
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
	"log"
	"os"
	"strings"
	"time"

	c "github.com/hashicorp/consul/api"
	ld "gopkg.in/launchdarkly/go-client.v4"
	"gopkg.in/launchdarkly/go-client.v4/utils"
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

// Internal implementation of the Consul-backed feature store. We don't export this - we just
// return an ld.FeatureStore.
type featureStore struct {
	config     c.Config
	prefix     string
	client     *c.Client
	cacheTTL   time.Duration
	logger     ld.Logger
	testTxHook func() // for unit testing of concurrent modifications
}

// FeatureStoreOption is the interface for optional configuration parameters that can be
// passed to NewConsulFeatureStore. These include UseConfig, Prefix, CacheTTL, and UseLogger.
type FeatureStoreOption interface {
	apply(store *featureStore) error
}

type configOption struct {
	config c.Config
}

func (o configOption) apply(store *featureStore) error {
	store.config = o.config
	return nil
}

// Config creates an option for NewConsulFeatureStore, to specify an entire configuration
// for the Consul driver. This overwrites any previous Consul settings that may have been
// specified.
//
//     store, err := ldconsul.NewConsulFeatureStore(ldconsul.Config(myConsulConfig))
func Config(config c.Config) FeatureStoreOption {
	return configOption{config}
}

type addressOption struct {
	address string
}

func (o addressOption) apply(store *featureStore) error {
	store.config.Address = o.address
	return nil
}

// Address creates an option for NewConsulFeatureStore, to set the address of the Consul server.
// If placed after Config(), this modifies the previously specified configuration.
//
//     store, err := ldconsul.NewConsulFeatureStore(ldconsul.Address("http://consulhost:8100"))
func Address(address string) FeatureStoreOption {
	return addressOption{address}
}

type prefixOption struct {
	prefix string
}

func (o prefixOption) apply(store *featureStore) error {
	store.prefix = o.prefix
	return nil
}

// Prefix creates an option for NewConsulFeatureStore, to specify a prefix for namespacing
// the feature store's keys. The default value is DefaultPrefix.
//
//     store, err := ldconsul.NewConsulFeatureStore(ldconsul.Prefix("ld-data"))
func Prefix(prefix string) FeatureStoreOption {
	return prefixOption{prefix}
}

type cacheTTLOption struct {
	ttl time.Duration
}

func (o cacheTTLOption) apply(store *featureStore) error {
	store.cacheTTL = o.ttl
	return nil
}

// CacheTTL creates an option for NewConsulFeatureStore, to specify how long flag data should be
// cached in memory to avoid rereading it from Consul. If this is zero, the feature store will
// not use an in-memory cache. The default value is DefaultCacheTTL.
//
//     store, err := ldconsul.NewConsulFeatureStore(ldconsul.CacheTTL(30*time.Second))
func CacheTTL(ttl time.Duration) FeatureStoreOption {
	return cacheTTLOption{ttl}
}

type loggerOption struct {
	logger ld.Logger
}

func (o loggerOption) apply(store *featureStore) error {
	store.logger = o.logger
	return nil
}

// Logger creates an option for NewConsulFeatureStore, to specify where to send log output.
// If not specified, a log.Logger is used.
//
//     store, err := ldconsul.NewConsulFeatureStore(ldconsul.Logger(myLogger))
func Logger(logger ld.Logger) FeatureStoreOption {
	return loggerOption{logger}
}

// NewConsulFeatureStore creates a new Consul-backed feature store with an optional memory cache. You
// may customize its behavior with any number of FeatureStoreOption values, such as Config, Address,
// Prefix, CacheTTL, and Logger.
func NewConsulFeatureStore(options ...FeatureStoreOption) (ld.FeatureStore, error) {
	store, err := newConsulFeatureStoreInternal(options...)
	if err != nil {
		return nil, err
	}
	return utils.NewFeatureStoreWrapper(store), nil
}

func newConsulFeatureStoreInternal(options ...FeatureStoreOption) (*featureStore, error) {
	store := &featureStore{
		config:   *c.DefaultConfig(),
		cacheTTL: DefaultCacheTTL,
	}
	for _, o := range options {
		err := o.apply(store)
		if err != nil {
			return nil, err
		}
	}

	if store.logger == nil {
		store.logger = defaultLogger()
	}
	if store.prefix == "" {
		store.prefix = DefaultPrefix
	}

	store.logger.Printf("ConsulFeatureStore: Using config: %+v", store.config)

	client, err := c.NewClient(&store.config)
	if err != nil {
		return nil, err
	}
	store.client = client
	return store, nil
}

func (store *featureStore) GetCacheTTL() time.Duration {
	return store.cacheTTL
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
		return results, err
	}

	for _, pair := range pairs {
		item, jsonErr := utils.UnmarshalItem(kind, pair.Value)

		if jsonErr != nil {
			return nil, err
		}

		results[item.GetKey()] = item
	}
	return results, nil
}

func (store *featureStore) InitInternal(allData map[ld.VersionedDataKind]map[string]ld.VersionedData) error {
	kv := store.client.KV()

	// Start by reading the existing keys; we will later delete any of these that weren't in allData.
	pairs, _, err := kv.List(store.prefix, nil)
	if err != nil {
		return err
	}
	oldKeys := make(map[string]bool)
	for _, p := range pairs {
		oldKeys[p.Key] = true
	}

	ops := make([]*c.KVTxnOp, 0)

	for kind, items := range allData {
		for k, v := range items {
			data, jsonErr := json.Marshal(v)
			if jsonErr != nil {
				return jsonErr
			}

			key := store.featureKeyFor(kind, k)
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
		return nil, jsonErr
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
		store.logger.Printf("ConsulFeatureStore: DEBUG: Concurrent modification detected, retrying")
	}
}

func (store *featureStore) InitializedInternal() bool {
	kv := store.client.KV()
	pair, _, err := kv.Get(store.initedKey(), nil)
	return pair != nil && err == nil
}

func defaultLogger() *log.Logger {
	return log.New(os.Stderr, "[LaunchDarkly]", log.LstdFlags)
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
		return nil, defaultModifyIndex, jsonErr
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
	return store.prefix + "/" + kind.GetNamespace()
}

func (store *featureStore) featureKeyFor(kind ld.VersionedDataKind, k string) string {
	return store.prefix + "/" + kind.GetNamespace() + "/" + k
}

func (store *featureStore) initedKey() string {
	return store.prefix + "/" + initedKey
}
