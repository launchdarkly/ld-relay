package ldconsul

import (
	"fmt"
	"strings"

	c "github.com/hashicorp/consul/api"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
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
	initedKey = "$inited"
)

// Internal implementation of the Consul-backed data store. We don't export this - we just
// return an ld.DataStore.
type consulDataStoreImpl struct {
	client     *c.Client
	prefix     string
	loggers    ldlog.Loggers
	testTxHook func() // for unit testing of concurrent modifications
}

func newConsulDataStoreImpl(builder *DataStoreBuilder, loggers ldlog.Loggers) (*consulDataStoreImpl, error) {
	loggers.Infof("Using config: %+v", builder.consulConfig)
	client, err := c.NewClient(&builder.consulConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to configure Consul client: %s", err)
	}
	return &consulDataStoreImpl{
		client:  client,
		prefix:  builder.prefix,
		loggers: loggers,
	}, nil
}

func (store *consulDataStoreImpl) Get(
	kind interfaces.StoreDataKind,
	key string,
) (interfaces.StoreSerializedItemDescriptor, error) {
	item, _, err := store.getInternal(kind, key)
	return item, err
}

func (store *consulDataStoreImpl) GetAll(
	kind interfaces.StoreDataKind,
) ([]interfaces.StoreKeyedSerializedItemDescriptor, error) {
	kv := store.client.KV()
	pairs, _, err := kv.List(store.collectionKey(kind), nil)

	if err != nil {
		return nil, fmt.Errorf("list failed for %s: %s", kind, err)
	}

	results := make([]interfaces.StoreKeyedSerializedItemDescriptor, 0, len(pairs))
	for _, pair := range pairs {
		results = append(results, interfaces.StoreKeyedSerializedItemDescriptor{
			Key:  store.itemKeyFromCombinedKey(kind, pair.Key),
			Item: interfaces.StoreSerializedItemDescriptor{SerializedItem: pair.Value},
		})
	}
	return results, nil
}

func (store *consulDataStoreImpl) Init(allData []interfaces.StoreSerializedCollection) error {
	kv := store.client.KV()

	// Start by reading the existing keys; we will later delete any of these that weren't in allData.
	pairs, _, err := kv.List(store.prefix, nil)
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
			key := store.combinedItemKey(coll.Kind, item.Key)
			op := &c.KVTxnOp{Verb: c.KVSet, Key: key, Value: item.Item.SerializedItem}
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

func (store *consulDataStoreImpl) Upsert(
	kind interfaces.StoreDataKind,
	key string,
	newItem interfaces.StoreSerializedItemDescriptor,
) (bool, error) {
	// We will potentially keep retrying to store indefinitely until someone's write succeeds
	for {
		// Get the item
		oldItem, modifyIndex, err := store.getInternal(kind, key)

		if err != nil {
			return false, err
		}

		// In this implementation, we have to parse the existing item in order to determine its version.
		oldVersion := oldItem.Version
		if oldItem.SerializedItem != nil {
			parsed, _ := kind.Deserialize(oldItem.SerializedItem)
			oldVersion = parsed.Version
		}

		// Check whether the item is stale. If so, don't do the update (and return the existing item to
		// DataStoreWrapper so it can be cached)
		if oldVersion >= newItem.Version {
			return false, nil
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
			Key:         store.combinedItemKey(kind, key),
			ModifyIndex: modifyIndex,
			Value:       newItem.SerializedItem,
		}
		written, _, err := kv.CAS(p, nil)

		if err != nil {
			return false, err // COVERAGE: can't simulate this condition in unit tests
		}

		if written {
			return true, nil // success
		}
		// If we failed, retry the whole shebang
		if store.loggers.IsDebugEnabled() { // COVERAGE: tests don't verify debug logging
			store.loggers.Debug("Concurrent modification detected, retrying")
		}
	}
}

func (store *consulDataStoreImpl) IsInitialized() bool {
	kv := store.client.KV()
	pair, _, err := kv.Get(store.initedKey(), nil)
	return pair != nil && err == nil
}

func (store *consulDataStoreImpl) IsStoreAvailable() bool {
	// Using a simple Get query here rather than the Consul Health API, because the latter seems to be
	// oriented toward monitoring of specific nodes or services; what we really want to know is just
	// whether a basic operation can succeed.
	kv := store.client.KV()
	_, _, err := kv.Get(store.initedKey(), nil)
	return err == nil
}

func (store *consulDataStoreImpl) Close() error {
	return nil
}

func (store *consulDataStoreImpl) getInternal(
	kind interfaces.StoreDataKind,
	key string,
) (
	retrievedItem interfaces.StoreSerializedItemDescriptor,
	modifyIndex uint64,
	err error,
) {
	var defaultModifyIndex = uint64(0)

	kv := store.client.KV()

	pair, _, err := kv.Get(store.combinedItemKey(kind, key), nil)

	if err != nil || pair == nil {
		return interfaces.StoreSerializedItemDescriptor{}.NotFound(), defaultModifyIndex, err
	}

	itemDesc := interfaces.StoreSerializedItemDescriptor{SerializedItem: pair.Value}
	return itemDesc, pair.ModifyIndex, nil
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
			// COVERAGE: can't simulate this condition in unit tests because we will only get this
			// far if the initial query in Init() already succeeded, and we don't have the ability
			// to make the Consul client fail *selectively* within a single test
			return err
		}
		if !ok { // COVERAGE: see above
			errs := make([]string, 0)
			for _, te := range resp.Errors { // COVERAGE: see above
				errs = append(errs, te.What)
			}
			//nolint:stylecheck // this error message is capitalized on purpose
			return fmt.Errorf("Consul transaction failed: %s", strings.Join(errs, ", ")) // COVERAGE: see above
		}
		i = j
	}
	return nil
}

func (store *consulDataStoreImpl) collectionKey(kind interfaces.StoreDataKind) string {
	return store.prefix + "/" + kind.GetName()
}

func (store *consulDataStoreImpl) combinedItemKey(kind interfaces.StoreDataKind, k string) string {
	return store.prefix + "/" + kind.GetName() + "/" + k
}

func (store *consulDataStoreImpl) itemKeyFromCombinedKey(kind interfaces.StoreDataKind, combinedKey string) string {
	return strings.TrimPrefix(combinedKey, store.collectionKey(kind)+"/")
}

func (store *consulDataStoreImpl) initedKey() string {
	return store.prefix + "/" + initedKey
}
