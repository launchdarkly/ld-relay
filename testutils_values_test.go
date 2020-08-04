package relay

import (
	"encoding/json"
	"time"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v6/config"
	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

type testEnv struct {
	name   string
	config config.EnvConfig
}

type testFlag struct {
	flag              ldmodel.FeatureFlag
	expectedValue     interface{}
	expectedVariation int
	expectedReason    map[string]interface{}
	isExperiment      bool
}

type unsupportedSDKCredential struct{} // implements config.SDKCredential

func (k unsupportedSDKCredential) GetAuthorizationHeaderValue() string { return "" }

// Returns a key matching the UUID header pattern
func key() config.MobileKey {
	return "mob-ffffffff-ffff-4fff-afff-ffffffffffff"
}

func user() string {
	return "eyJrZXkiOiJ0ZXN0In0="
}

const (
	// The "undefined" values are well-formed, but do not match any environment in our test data.
	undefinedSDKKey    = config.SDKKey("sdk-99999999-9999-4999-8999-999999999999")
	undefinedMobileKey = config.MobileKey("mob-99999999-9999-4999-8999-999999999999")
	undefinedEnvID     = config.EnvironmentID("999999999999999999999999")

	// The "malformed" values contain an unsupported authorization scheme.
	malformedSDKKey    = config.SDKKey("fake_key sdk-99999999-9999-4999-8999-999999999999")
	malformedMobileKey = config.MobileKey("fake_key mob-99999999-9999-4999-8999-999999999999")
)

var testEnvMain = testEnv{
	name: "sdk test",
	config: config.EnvConfig{
		SDKKey: config.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d0"),
	},
}

var testEnvWithTTL = testEnv{
	name: "sdk test with TTL",
	config: config.EnvConfig{
		SDKKey: c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d5"),
		TTL:    ct.NewOptDuration(10 * time.Minute),
	},
}

var testEnvMobile = testEnv{
	name: "mobile test",
	config: config.EnvConfig{
		SDKKey:    c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d2"),
		MobileKey: c.MobileKey("mob-98e2b0b4-2688-4a59-9810-1e0e3d7e42db"),
	},
}

var testEnvClientSide = testEnv{
	name: "JS client-side test",
	config: config.EnvConfig{
		SDKKey: c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d1"),
		EnvID:  c.EnvironmentID("507f1f77bcf86cd799439011"),
	},
}

var testEnvClientSideSecureMode = testEnv{
	name: "JS client-side test with secure mode",
	config: config.EnvConfig{
		SDKKey:     c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d9"),
		EnvID:      c.EnvironmentID("507f1f77bcf86cd799439019"),
		SecureMode: true,
	},
}

func makeEnvConfigs(envs ...testEnv) map[string]*config.EnvConfig {
	ret := make(map[string]*config.EnvConfig)
	for _, e := range envs {
		c := e.config
		ret[e.name] = &c
	}
	return ret
}

var flag1ServerSide = testFlag{
	flag:              ldbuilders.NewFlagBuilder("some-flag-key").OffVariation(0).Variations(ldvalue.Bool(true)).Version(2).Build(),
	expectedValue:     true,
	expectedVariation: 0,
	expectedReason:    map[string]interface{}{"kind": "OFF"},
}
var flag2ServerSide = testFlag{
	flag:              ldbuilders.NewFlagBuilder("another-flag-key").On(true).FallthroughVariation(0).Variations(ldvalue.Int(3)).Version(1).Build(),
	expectedValue:     3,
	expectedVariation: 0,
	expectedReason:    map[string]interface{}{"kind": "FALLTHROUGH"},
}
var flag3ServerSide = testFlag{
	flag:           ldbuilders.NewFlagBuilder("off-variation-key").Version(3).Build(),
	expectedValue:  nil,
	expectedReason: map[string]interface{}{"kind": "OFF"},
}
var flag4ClientSide = testFlag{
	flag:              ldbuilders.NewFlagBuilder("client-flag-key").OffVariation(0).Variations(ldvalue.Int(5)).Version(2).ClientSide(true).Build(),
	expectedValue:     5,
	expectedVariation: 0,
	expectedReason:    map[string]interface{}{"kind": "OFF"},
}
var flag5ClientSide = testFlag{
	flag: ldbuilders.NewFlagBuilder("fallthrough-experiment-flag-key").On(true).FallthroughVariation(0).Variations(ldvalue.Int(3)).
		TrackEventsFallthrough(true).ClientSide(true).Version(1).Build(),
	expectedValue:  3,
	expectedReason: map[string]interface{}{"kind": "FALLTHROUGH"},
	isExperiment:   true,
}
var flag6ClientSide = testFlag{
	flag: ldbuilders.NewFlagBuilder("rule-match-experiment-flag-key").On(true).
		AddRule(ldbuilders.NewRuleBuilder().ID("rule-id").Variation(0).TrackEvents(true).
			Clauses(ldbuilders.Negate(ldbuilders.Clause(lduser.KeyAttribute, ldmodel.OperatorIn, ldvalue.String("not-a-real-user-key"))))).
		Variations(ldvalue.Int(4)).ClientSide(true).Version(1).Build(),
	expectedValue:  4,
	expectedReason: map[string]interface{}{"kind": "RULE_MATCH", "ruleIndex": 0, "ruleId": "rule-id"},
	isExperiment:   true,
}
var allFlags = []testFlag{flag1ServerSide, flag2ServerSide, flag3ServerSide, flag4ClientSide,
	flag5ClientSide, flag6ClientSide}
var clientSideFlags = []testFlag{flag4ClientSide, flag5ClientSide, flag6ClientSide}

var segment1 = ldbuilders.NewSegmentBuilder("segment-key").Build()

var allData = []ldstoretypes.Collection{
	{
		Kind: ldstoreimpl.Features(),
		Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: flag1ServerSide.flag.Key, Item: sharedtest.FlagDesc(flag1ServerSide.flag)},
			{Key: flag2ServerSide.flag.Key, Item: sharedtest.FlagDesc(flag2ServerSide.flag)},
			{Key: flag3ServerSide.flag.Key, Item: sharedtest.FlagDesc(flag3ServerSide.flag)},
			{Key: flag4ClientSide.flag.Key, Item: sharedtest.FlagDesc(flag4ClientSide.flag)},
			{Key: flag5ClientSide.flag.Key, Item: sharedtest.FlagDesc(flag5ClientSide.flag)},
			{Key: flag6ClientSide.flag.Key, Item: sharedtest.FlagDesc(flag6ClientSide.flag)},
		},
	},
	{
		Kind: ldstoreimpl.Segments(),
		Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: segment1.Key, Item: sharedtest.SegmentDesc(segment1)},
		},
	},
}

func flagsMap(testFlags []testFlag) map[string]interface{} {
	ret := make(map[string]interface{})
	for _, f := range testFlags {
		ret[f.flag.Key] = f.flag
	}
	return ret
}

func makeEvalBody(flags []testFlag, fullData bool, reasons bool) string {
	obj := make(map[string]interface{})
	for _, f := range flags {
		value := f.expectedValue
		if fullData {
			m := map[string]interface{}{"value": value, "version": f.flag.Version}
			if value != nil {
				m["variation"] = f.expectedVariation
			}
			if reasons || f.isExperiment {
				m["reason"] = f.expectedReason
			}
			if f.flag.TrackEvents || f.isExperiment {
				m["trackEvents"] = true
			}
			if f.isExperiment {
				m["trackReason"] = true
			}
			value = m
		}
		obj[f.flag.Key] = value
	}
	out, _ := json.Marshal(obj)
	return string(out)
}
