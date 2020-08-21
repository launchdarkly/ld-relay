module github.com/launchdarkly/ld-relay/v6

go 1.13

require (
	github.com/go-gcfg/gcfg v1.2.3
	github.com/gorilla/context v0.0.0-20160226214623-1ea25387ff6f // indirect
	github.com/hashicorp/golang-lru v0.5.3 // indirect
	github.com/kardianos/minwinsvc v0.0.0-20151122163309-cad6b2b879b0
	github.com/launchdarkly/eventsource v1.6.1
	github.com/launchdarkly/go-configtypes v1.0.0
	github.com/launchdarkly/go-test-helpers/v2 v2.2.0
	github.com/launchdarkly/ld-relay-config v0.0.0-00010101000000-000000000000
	github.com/launchdarkly/ld-relay-core v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.6.1
	gopkg.in/launchdarkly/go-sdk-common.v2 v2.0.0-beta.3
	gopkg.in/launchdarkly/go-server-sdk.v5 v5.0.0-beta.6
)

replace github.com/launchdarkly/ld-relay-config => github.com/launchdarkly/ld-relay-config-private v0.0.0-20200819003132-defe927c1385

replace github.com/launchdarkly/ld-relay-core => github.com/launchdarkly/ld-relay-core-private v0.0.0-20200821200653-4e216125e7b1
