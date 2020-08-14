package sharedtest

import (
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

type TestFlag struct {
	Flag              ldmodel.FeatureFlag
	ExpectedValue     interface{}
	ExpectedVariation int
	ExpectedReason    map[string]interface{}
	IsExperiment      bool
}

var Flag1ServerSide = TestFlag{
	Flag:              ldbuilders.NewFlagBuilder("some-flag-key").OffVariation(0).Variations(ldvalue.Bool(true)).Version(2).Build(),
	ExpectedValue:     true,
	ExpectedVariation: 0,
	ExpectedReason:    map[string]interface{}{"kind": "OFF"},
}
var Flag2ServerSide = TestFlag{
	Flag:              ldbuilders.NewFlagBuilder("another-flag-key").On(true).FallthroughVariation(0).Variations(ldvalue.Int(3)).Version(1).Build(),
	ExpectedValue:     3,
	ExpectedVariation: 0,
	ExpectedReason:    map[string]interface{}{"kind": "FALLTHROUGH"},
}
var Flag3ServerSideNotMobile = TestFlag{
	Flag:           ldbuilders.NewFlagBuilder("off-variation-key").Version(3).ClientSideUsingMobileKey(false).Build(),
	ExpectedValue:  nil,
	ExpectedReason: map[string]interface{}{"kind": "OFF"},
}
var Flag4ClientSide = TestFlag{
	Flag: ldbuilders.NewFlagBuilder("client-flag-key").OffVariation(0).Variations(ldvalue.Int(5)).Version(2).
		ClientSideUsingEnvironmentID(true).Build(),
	ExpectedValue:     5,
	ExpectedVariation: 0,
	ExpectedReason:    map[string]interface{}{"kind": "OFF"},
}
var Flag5ClientSide = TestFlag{
	Flag: ldbuilders.NewFlagBuilder("fallthrough-experiment-flag-key").On(true).FallthroughVariation(0).Variations(ldvalue.Int(3)).
		TrackEventsFallthrough(true).ClientSideUsingEnvironmentID(true).Version(1).Build(),
	ExpectedValue:  3,
	ExpectedReason: map[string]interface{}{"kind": "FALLTHROUGH"},
	IsExperiment:   true,
}
var Flag6ClientSideNotMobile = TestFlag{
	Flag: ldbuilders.NewFlagBuilder("rule-match-experiment-flag-key").On(true).
		AddRule(ldbuilders.NewRuleBuilder().ID("rule-id").Variation(0).TrackEvents(true).
			Clauses(ldbuilders.Negate(ldbuilders.Clause(lduser.KeyAttribute, ldmodel.OperatorIn, ldvalue.String("not-a-real-user-key"))))).
		Variations(ldvalue.Int(4)).ClientSideUsingEnvironmentID(true).ClientSideUsingMobileKey(false).Version(1).Build(),
	ExpectedValue:  4,
	ExpectedReason: map[string]interface{}{"kind": "RULE_MATCH", "ruleIndex": 0, "ruleId": "rule-id"},
	IsExperiment:   true,
}
var Flag7Mobile = TestFlag{
	Flag: ldbuilders.NewFlagBuilder("mobile-flag-key").OffVariation(0).Variations(ldvalue.Int(5)).Version(2).
		ClientSideUsingMobileKey(true).Build(),
	ExpectedValue:     5,
	ExpectedVariation: 0,
	ExpectedReason:    map[string]interface{}{"kind": "OFF"},
}
var AllFlags = []TestFlag{Flag1ServerSide, Flag2ServerSide, Flag3ServerSideNotMobile, Flag4ClientSide,
	Flag5ClientSide, Flag6ClientSideNotMobile, Flag7Mobile}
var ClientSideFlags = []TestFlag{Flag4ClientSide, Flag5ClientSide, Flag6ClientSideNotMobile}
var MobileFlags = []TestFlag{Flag1ServerSide, Flag2ServerSide, Flag4ClientSide, Flag5ClientSide, Flag7Mobile}

var Segment1 = ldbuilders.NewSegmentBuilder("segment-key").Build()

var AllData = []ldstoretypes.Collection{
	{
		Kind: ldstoreimpl.Features(),
		Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: Flag1ServerSide.Flag.Key, Item: FlagDesc(Flag1ServerSide.Flag)},
			{Key: Flag2ServerSide.Flag.Key, Item: FlagDesc(Flag2ServerSide.Flag)},
			{Key: Flag3ServerSideNotMobile.Flag.Key, Item: FlagDesc(Flag3ServerSideNotMobile.Flag)},
			{Key: Flag4ClientSide.Flag.Key, Item: FlagDesc(Flag4ClientSide.Flag)},
			{Key: Flag5ClientSide.Flag.Key, Item: FlagDesc(Flag5ClientSide.Flag)},
			{Key: Flag6ClientSideNotMobile.Flag.Key, Item: FlagDesc(Flag6ClientSideNotMobile.Flag)},
			{Key: Flag7Mobile.Flag.Key, Item: FlagDesc(Flag7Mobile.Flag)},
		},
	},
	{
		Kind: ldstoreimpl.Segments(),
		Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: Segment1.Key, Item: SegmentDesc(Segment1)},
		},
	},
}

func FlagsMap(testFlags []TestFlag) map[string]interface{} {
	ret := make(map[string]interface{})
	for _, f := range testFlags {
		ret[f.Flag.Key] = f.Flag
	}
	return ret
}
