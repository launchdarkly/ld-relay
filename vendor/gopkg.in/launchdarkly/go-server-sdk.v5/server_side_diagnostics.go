package ldclient

import (
	"time"

	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

func createDiagnosticsManager(sdkKey string, config Config, waitFor time.Duration) *ldevents.DiagnosticsManager {
	id := ldevents.NewDiagnosticID(sdkKey)
	return ldevents.NewDiagnosticsManager(
		id,
		makeDiagnosticConfigData(config, waitFor),
		makeDiagnosticSDKData(),
		time.Now(),
		nil,
	)
}

func makeDiagnosticConfigData(config Config, waitFor time.Duration) ldvalue.Value {
	builder := ldvalue.ObjectBuild().
		Set("startWaitMillis", durationToMillis(waitFor))

	// Allow each pluggable component to describe its own relevant properties.
	mergeComponentProperties(builder, config.HTTP, ldcomponents.HTTPConfiguration(), "")
	mergeComponentProperties(builder, config.DataSource, ldcomponents.StreamingDataSource(), "")
	mergeComponentProperties(builder, config.DataStore, ldcomponents.InMemoryDataStore(), "dataStoreType")
	mergeComponentProperties(builder, config.Events, ldcomponents.SendEvents(), "")

	return builder.Build()
}

var allowedDiagnosticComponentProperties = map[string]ldvalue.ValueType{ //nolint:gochecknoglobals
	"allAttributesPrivate":              ldvalue.BoolType,
	"connectTimeoutMillis":              ldvalue.NumberType,
	"customBaseURI":                     ldvalue.BoolType,
	"customEventsURI":                   ldvalue.BoolType,
	"customStreamURI":                   ldvalue.BoolType,
	"diagnosticRecordingIntervalMillis": ldvalue.NumberType,
	"eventsCapacity":                    ldvalue.NumberType,
	"eventsFlushIntervalMillis":         ldvalue.NumberType,
	"inlineUsersInEvents":               ldvalue.BoolType,
	"pollingIntervalMillis":             ldvalue.NumberType,
	"reconnectTimeMillis":               ldvalue.NumberType,
	"socketTimeoutMillis":               ldvalue.NumberType,
	"streamingDisabled":                 ldvalue.BoolType,
	"userKeysCapacity":                  ldvalue.NumberType,
	"userKeysFlushIntervalMillis":       ldvalue.NumberType,
	"usingProxy":                        ldvalue.BoolType,
	"usingRelayDaemon":                  ldvalue.BoolType,
}

// Attempts to add relevant configuration properties, if any, from a customizable component:
// - If the component does not implement DiagnosticDescription, set the defaultPropertyName property to
//   "custom".
// - If it does implement DiagnosticDescription, call its DescribeConfiguration() method to get a value.
// - If the value is a string, then set the defaultPropertyName property to that value.
// - If the value is an object, then copy all of its properties as long as they are ones we recognize
//   and have the expected type.
func mergeComponentProperties(
	builder ldvalue.ObjectBuilder,
	component interface{},
	defaultComponent interface{},
	defaultPropertyName string,
) {
	if component == nil {
		component = defaultComponent
	}
	if dd, ok := component.(interfaces.DiagnosticDescription); ok {
		componentDesc := dd.DescribeConfiguration()
		if !componentDesc.IsNull() {
			if componentDesc.Type() == ldvalue.StringType && defaultPropertyName != "" {
				builder.Set(defaultPropertyName, componentDesc)
			} else if componentDesc.Type() == ldvalue.ObjectType {
				componentDesc.Enumerate(func(i int, name string, value ldvalue.Value) bool {
					if allowedType, ok := allowedDiagnosticComponentProperties[name]; ok {
						if value.IsNull() || value.Type() == allowedType {
							builder.Set(name, value)
						}
					}
					return true
				})
			}
		}
	} else if defaultPropertyName != "" {
		builder.Set(defaultPropertyName, ldvalue.String("custom"))
	}
}

func makeDiagnosticSDKData() ldvalue.Value {
	return ldvalue.ObjectBuild().
		Set("name", ldvalue.String("go-server-sdk")).
		Set("version", ldvalue.String(Version)).
		Build()
}

func durationToMillis(d time.Duration) ldvalue.Value {
	return ldvalue.Float64(float64(uint64(d / time.Millisecond)))
}
