module github.com/launchdarkly/ld-relay/v6

go 1.16

require (
	cloud.google.com/go v0.81.0 // indirect
	contrib.go.opencensus.io/exporter/prometheus v0.4.0
	contrib.go.opencensus.io/exporter/stackdriver v0.13.6
	github.com/DataDog/datadog-go v3.7.2+incompatible // indirect
	github.com/DataDog/opencensus-go-exporter-datadog v0.0.0-20210527074920-9baf37265e83
	github.com/antihax/optional v1.0.0
	github.com/aws/aws-sdk-go v1.40.45 // indirect
	github.com/aws/aws-sdk-go-v2 v1.16.14
	github.com/aws/aws-sdk-go-v2/config v1.17.5
	github.com/aws/aws-sdk-go-v2/credentials v1.12.18
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.16.4
	github.com/cyphar/filepath-securejoin v0.2.3
	github.com/fatih/color v1.12.0 // indirect
	github.com/fsnotify/fsnotify v1.4.9
	github.com/go-kit/log v0.2.0 // indirect
	github.com/go-redis/redis/v8 v8.8.0
	github.com/gomodule/redigo v1.8.2
	github.com/google/btree v1.0.1 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/gorilla/mux v1.8.0
	github.com/gregjones/httpcache v0.0.0-20180305231024-9cad4c3443a7
	github.com/hashicorp/consul/api v1.15.3
	github.com/hashicorp/errwrap v1.1.0 // indirect
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
	github.com/mattn/go-isatty v0.0.14 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.2-0.20181231171920-c182affec369 // indirect
	github.com/miekg/dns v1.1.43 // indirect
	github.com/mitchellh/mapstructure v1.4.2 // indirect
	github.com/onsi/gomega v1.15.0 // indirect
	github.com/pborman/uuid v1.2.0
	github.com/prometheus/client_golang v1.12.1 // indirect
	github.com/stretchr/objx v0.2.0 // indirect
	github.com/stretchr/testify v1.7.0
	go.opencensus.io v0.23.0
	go.opentelemetry.io/otel v1.4.1 // indirect
	go.opentelemetry.io/otel/internal/metric v0.27.0 // indirect
	golang.org/x/crypto v0.0.0-20220411220226-7b82a4e95df4 // indirect
	golang.org/x/net v0.0.0-20220906165146-f3363e06e74c // indirect; indirect // override to address CVE-2022-27664
	golang.org/x/oauth2 v0.0.0-20210819190943-2bc19b11175f // indirect
	golang.org/x/sync v0.0.0-20220722155255-886fb9371eb4
	golang.org/x/text v0.3.8 // indirect; override to address CVE-2022-32149
	google.golang.org/genproto v0.0.0-20211208223120-3a66f561d7aa // indirect
	google.golang.org/grpc v1.45.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/gcfg.v1 v1.2.3
	gopkg.in/launchdarkly/go-jsonstream.v1 v1.0.1
	gopkg.in/launchdarkly/go-sdk-common.v2 v2.4.0
	gopkg.in/launchdarkly/go-sdk-events.v1 v1.1.1
	gopkg.in/launchdarkly/go-server-sdk-evaluation.v1 v1.5.0
	gopkg.in/launchdarkly/go-server-sdk.v5 v5.9.0
	gopkg.in/warnings.v0 v0.1.2 // indirect
	gopkg.in/yaml.v3 v3.0.0 // indirect
)
