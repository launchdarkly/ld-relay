package store

import (
	consul "github.com/hashicorp/consul/api"
)

// ConsulInitChecker implements StoreInitChecker by directly querying Consul
// for the $inited KV key, bypassing the SDK's caching layer.
type ConsulInitChecker struct {
	client *consul.Client
	prefix string
}

// NewConsulInitChecker creates a checker that connects to Consul at the given address.
// The prefix should match the store prefix used by the SDK (e.g. "launchdarkly").
func NewConsulInitChecker(address string, token string, tokenFile string, prefix string) (*ConsulInitChecker, error) {
	config := consul.DefaultConfig()
	config.Address = address
	if token != "" {
		config.Token = token
	} else if tokenFile != "" {
		config.TokenFile = tokenFile
	}
	client, err := consul.NewClient(config)
	if err != nil {
		return nil, err
	}
	return &ConsulInitChecker{
		client: client,
		prefix: prefix,
	}, nil
}

func (c *ConsulInitChecker) initedKey() string {
	return c.prefix + "/$inited"
}

// CheckInitialized checks if the $inited key exists in Consul KV.
func (c *ConsulInitChecker) CheckInitialized() (available bool, initialized bool, err error) {
	pair, _, err := c.client.KV().Get(c.initedKey(), nil)
	if err != nil {
		return false, false, err
	}
	return true, pair != nil, nil
}

// Close is a no-op for Consul (the HTTP client doesn't need explicit cleanup).
func (c *ConsulInitChecker) Close() error {
	return nil
}
