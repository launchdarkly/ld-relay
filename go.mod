module github.com/launchdarkly/ld-relay/v6

go 1.17

require (
	contrib.go.opencensus.io/exporter/prometheus v0.4.0
	github.com/DataDog/opencensus-go-exporter-datadog v0.0.0-20210527074920-9baf37265e83
	github.com/antihax/optional v1.0.0
	github.com/aws/aws-sdk-go-v2 v1.16.14
	github.com/aws/aws-sdk-go-v2/config v1.17.5
	github.com/aws/aws-sdk-go-v2/credentials v1.12.18
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.16.4
	github.com/cyphar/filepath-securejoin v0.2.3
	github.com/fsnotify/fsnotify v1.5.1
	github.com/go-redis/redis/v8 v8.8.0
	github.com/gomodule/redigo v1.8.9
	github.com/gorilla/mux v1.8.0
	github.com/gregjones/httpcache v0.0.0-20180305231024-9cad4c3443a7
	github.com/hashicorp/consul/api v1.15.3
	github.com/kardianos/minwinsvc v0.0.0-20151122163309-cad6b2b879b0
	github.com/launchdarkly/api-client-go/v12 v12.0.0
	github.com/launchdarkly/eventsource v1.7.1
	github.com/launchdarkly/go-configtypes v1.1.0
	github.com/launchdarkly/go-server-sdk-consul v1.0.2
	github.com/launchdarkly/go-server-sdk-dynamodb/v2 v2.0.1
	github.com/launchdarkly/go-server-sdk-redis-redigo v1.2.2
	github.com/launchdarkly/go-test-helpers/v2 v2.3.1
	github.com/launchdarkly/opencensus-go-exporter-stackdriver v0.14.2
	github.com/pborman/uuid v1.2.0
	github.com/stretchr/testify v1.7.1
	go.opencensus.io v0.23.0
	golang.org/x/sync v0.0.0-20220722155255-886fb9371eb4
	gopkg.in/gcfg.v1 v1.2.3
	gopkg.in/launchdarkly/go-jsonstream.v1 v1.0.1
	gopkg.in/launchdarkly/go-sdk-common.v2 v2.5.1
	gopkg.in/launchdarkly/go-sdk-events.v1 v1.1.1
	gopkg.in/launchdarkly/go-server-sdk-evaluation.v1 v1.5.0
	gopkg.in/launchdarkly/go-server-sdk.v5 v5.10.1
)

require (
	cloud.google.com/go v0.75.0 // indirect
	github.com/DataDog/datadog-go v3.7.2+incompatible // indirect
	github.com/armon/go-metrics v0.3.10 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.12.15 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.1.21 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.4.15 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.3.22 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.9.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.7.15 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.9.15 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.11.21 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.13.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.16.17 // indirect
	github.com/aws/smithy-go v1.13.2 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/census-instrumentation/opencensus-proto v0.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.1.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/fatih/color v1.9.0 // indirect
	github.com/go-kit/log v0.1.0 // indirect
	github.com/go-logfmt/logfmt v0.5.0 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.4.3 // indirect
	github.com/google/go-cmp v0.5.8 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/googleapis/gax-go/v2 v2.0.5 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-hclog v0.16.2 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/go-rootcerts v1.0.2 // indirect
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/hashicorp/serf v0.9.7 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/launchdarkly/ccache v1.1.0 // indirect
	github.com/launchdarkly/go-ntlm-proxy-auth v1.0.1 // indirect
	github.com/launchdarkly/go-ntlmssp v1.0.1 // indirect
	github.com/launchdarkly/go-semver v1.0.2 // indirect
	github.com/mailru/easyjson v0.7.6 // indirect
	github.com/mattn/go-colorable v0.1.6 // indirect
	github.com/mattn/go-isatty v0.0.12 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/mapstructure v1.4.1 // indirect
	github.com/patrickmn/go-cache v2.1.0+incompatible // indirect
	github.com/philhofer/fwd v1.0.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_golang v1.11.1 // indirect; override to address CVE-2022-21698
	github.com/prometheus/client_model v0.2.0 // indirect
	github.com/prometheus/common v0.28.0 // indirect
	github.com/prometheus/procfs v0.6.0 // indirect
	github.com/prometheus/statsd_exporter v0.21.0 // indirect
	github.com/tinylib/msgp v1.1.2 // indirect
	go.opentelemetry.io/otel v0.19.0 // indirect
	go.opentelemetry.io/otel/metric v0.19.0 // indirect
	go.opentelemetry.io/otel/trace v0.19.0 // indirect
	golang.org/x/crypto v0.0.0-20220411220226-7b82a4e95df4 // indirect
	golang.org/x/net v0.7.0 // indirect; override to address CVE-2022-41723
	golang.org/x/oauth2 v0.0.0-20210514164344-f6687ab2804c // indirect
	golang.org/x/sys v0.5.0 // indirect
	golang.org/x/text v0.7.0 // indirect
	google.golang.org/api v0.37.0 // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/genproto v0.0.0-20210126160654-44e461bb6506 // indirect
	google.golang.org/grpc v1.35.0 // indirect
	google.golang.org/protobuf v1.26.0-rc.1 // indirect
	gopkg.in/DataDog/dd-trace-go.v1 v1.22.0 // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.0 // indirect
)
