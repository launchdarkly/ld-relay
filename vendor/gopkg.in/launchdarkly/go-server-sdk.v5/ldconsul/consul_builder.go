package ldconsul

import (
	c "github.com/hashicorp/consul/api"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

const (
	// DefaultPrefix is a string that is prepended (along with a slash) to all Consul keys used
	// by the data store. You can change this value with the Prefix() option.
	DefaultPrefix = "launchdarkly"
)

// DataStoreBuilder is a builder for configuring the Consul-based persistent data store.
//
// Obtain an instance of this type by calling DataStore(). After calling its methods to specify any
// desired custom settings, wrap it in a PersistentDataStoreBuilder by calling
// ldcomponents.PersistentDataStore(), and then store this in the SDK configuration's DataStore field.
//
// Builder calls can be chained, for example:
//
//     config.DataStore = ldconsul.DataStore().Address("hostname:8500).Prefix("prefix")
//
// You do not need to call the builder's CreatePersistentDataStore() method yourself to build the
// actual data store; that will be done by the SDK.
type DataStoreBuilder struct {
	consulConfig c.Config
	prefix       string
}

// DataStore returns a configurable builder for a Consul-backed data store.
func DataStore() *DataStoreBuilder {
	return &DataStoreBuilder{
		prefix: DefaultPrefix,
	}
}

// Config specifies an entire configuration for the Consul driver. This overwrites any previous Consul
// settings that may have been specified.
func (b *DataStoreBuilder) Config(consulConfig c.Config) *DataStoreBuilder {
	b.consulConfig = consulConfig
	return b
}

// Address sets the address of the Consul server. If placed after Config(), this modifies the preivously
// specified configuration.
func (b *DataStoreBuilder) Address(address string) *DataStoreBuilder {
	b.consulConfig.Address = address
	return b
}

// Prefix specifies a prefix for namespacing the data store's keys. The default value is DefaultPrefix.
func (b *DataStoreBuilder) Prefix(prefix string) *DataStoreBuilder {
	if prefix == "" {
		prefix = DefaultPrefix
	}
	b.prefix = prefix
	return b
}

// CreatePersistentDataStore is called internally by the SDK to create the data store implementation object.
func (b *DataStoreBuilder) CreatePersistentDataStore(
	context interfaces.ClientContext,
) (interfaces.PersistentDataStore, error) {
	store, err := newConsulDataStoreImpl(b, context.GetLogging().GetLoggers())
	return store, err
}

// DescribeConfiguration is used internally by the SDK to inspect the configuration.
func (b *DataStoreBuilder) DescribeConfiguration() ldvalue.Value {
	return ldvalue.String("Consul")
}
