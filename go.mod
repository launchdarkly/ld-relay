module github.com/launchdarkly/ld-relay/v6

go 1.13

require (
	contrib.go.opencensus.io/exporter/prometheus v0.3.0
	contrib.go.opencensus.io/exporter/stackdriver v0.13.6
	github.com/DataDog/datadog-go v3.7.2+incompatible // indirect
	github.com/DataDog/opencensus-go-exporter-datadog v0.0.0-20210527074920-9baf37265e83
	github.com/antihax/optional v1.0.0
	github.com/aws/aws-sdk-go v1.37.2
	github.com/fsnotify/fsnotify v1.4.7
	github.com/gomodule/redigo v1.8.2
	github.com/gorilla/mux v1.8.0
	github.com/gregjones/httpcache v0.0.0-20171119193500-2bcd89a1743f
	github.com/hashicorp/consul/api v1.5.0
	github.com/kardianos/minwinsvc v0.0.0-20151122163309-cad6b2b879b0
	github.com/launchdarkly/api-client-go v3.7.0+incompatible
	github.com/launchdarkly/eventsource v1.6.2
	github.com/launchdarkly/go-configtypes v1.1.0
	github.com/launchdarkly/go-server-sdk-consul v1.0.0
	github.com/launchdarkly/go-server-sdk-dynamodb v1.0.1
	github.com/launchdarkly/go-server-sdk-redis-redigo v1.0.0
	github.com/launchdarkly/go-test-helpers/v2 v2.2.0
	github.com/pborman/uuid v1.2.0
	github.com/stretchr/testify v1.6.1
	go.opencensus.io v0.23.0
	gopkg.in/gcfg.v1 v1.2.3
	gopkg.in/launchdarkly/go-jsonstream.v1 v1.0.1
	gopkg.in/launchdarkly/go-sdk-common.v2 v2.3.0
	gopkg.in/launchdarkly/go-sdk-events.v1 v1.1.1
	gopkg.in/launchdarkly/go-server-sdk-evaluation.v1 v1.3.0
	gopkg.in/launchdarkly/go-server-sdk.v5 v5.4.0
)
