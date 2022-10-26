module github.com/launchdarkly/ld-relay/v6

go 1.16

require (
	contrib.go.opencensus.io/exporter/prometheus v0.4.0
	github.com/DataDog/datadog-go v3.7.2+incompatible // indirect
	github.com/DataDog/opencensus-go-exporter-datadog v0.0.0-20210527074920-9baf37265e83
	github.com/antihax/optional v1.0.0
	github.com/aws/aws-sdk-go-v2 v1.16.14
	github.com/aws/aws-sdk-go-v2/config v1.17.5
	github.com/aws/aws-sdk-go-v2/credentials v1.12.18
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.16.4
	github.com/cyphar/filepath-securejoin v0.2.3
	github.com/fsnotify/fsnotify v1.5.1
	github.com/go-redis/redis/v8 v8.8.0
	github.com/gomodule/redigo v1.8.2
	github.com/google/uuid v1.3.0 // indirect
	github.com/gorilla/mux v1.8.0
	github.com/gregjones/httpcache v0.0.0-20180305231024-9cad4c3443a7
	github.com/hashicorp/consul/api v1.15.3
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-hclog v0.16.2 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/kardianos/minwinsvc v0.0.0-20151122163309-cad6b2b879b0
	github.com/launchdarkly/api-client-go v5.0.3+incompatible
	github.com/launchdarkly/eventsource v1.7.1
	github.com/launchdarkly/go-configtypes v1.1.0
	github.com/launchdarkly/go-server-sdk-consul v1.0.2
	github.com/launchdarkly/go-server-sdk-dynamodb/v2 v2.0.0
	github.com/launchdarkly/go-server-sdk-redis-redigo v1.2.1
	github.com/launchdarkly/go-test-helpers/v2 v2.3.1
	github.com/launchdarkly/opencensus-go-exporter-stackdriver v0.14.2
	github.com/pborman/uuid v1.2.0
	github.com/prometheus/client_golang v1.11.1 // indirect; override to address CVE-2022-21698
	github.com/stretchr/testify v1.7.1
	go.opencensus.io v0.23.0
	golang.org/x/crypto v0.0.0-20220411220226-7b82a4e95df4 // indirect
	golang.org/x/net v0.0.0-20220906165146-f3363e06e74c // indirect; override to address CVE-2022-27664
	golang.org/x/sync v0.0.0-20220722155255-886fb9371eb4
	golang.org/x/text v0.3.8 // indirect; override to address CVE-2022-32149
	gopkg.in/gcfg.v1 v1.2.3
	gopkg.in/launchdarkly/go-jsonstream.v1 v1.0.1
	gopkg.in/launchdarkly/go-sdk-common.v2 v2.4.0
	gopkg.in/launchdarkly/go-sdk-events.v1 v1.1.1
	gopkg.in/launchdarkly/go-server-sdk-evaluation.v1 v1.5.0
	gopkg.in/launchdarkly/go-server-sdk.v5 v5.9.0
	gopkg.in/warnings.v0 v0.1.2 // indirect
	gopkg.in/yaml.v3 v3.0.0 // indirect
)
