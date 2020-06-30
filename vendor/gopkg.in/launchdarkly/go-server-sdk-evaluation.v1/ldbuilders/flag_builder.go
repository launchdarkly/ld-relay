package ldbuilders

import (
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
)

const (
	// NoVariation represents the lack of a variation index (for FlagBuilder.OffVariation, etc.).
	NoVariation = -1
)

// Bucket constructs a WeightedVariation with the specified variation index and weight.
func Bucket(variationIndex int, weight int) ldmodel.WeightedVariation {
	return ldmodel.WeightedVariation{Variation: variationIndex, Weight: weight}
}

// Rollout constructs a VariationOrRollout with the specified buckets.
func Rollout(buckets ...ldmodel.WeightedVariation) ldmodel.VariationOrRollout {
	return ldmodel.VariationOrRollout{Rollout: &ldmodel.Rollout{Variations: buckets}}
}

// Variation constructs a VariationOrRollout with the specified variation index.
func Variation(variationIndex int) ldmodel.VariationOrRollout {
	return ldmodel.VariationOrRollout{Variation: &variationIndex}
}

// FlagBuilder provides a builder pattern for FeatureFlag.
type FlagBuilder struct {
	flag ldmodel.FeatureFlag
}

// RuleBuilder provides a builder pattern for FlagRule.
type RuleBuilder struct {
	rule ldmodel.FlagRule
}

// NewFlagBuilder creates a FlagBuilder.
func NewFlagBuilder(key string) *FlagBuilder {
	return &FlagBuilder{flag: ldmodel.FeatureFlag{Key: key}}
}

// Build returns the configured FeatureFlag.
func (b *FlagBuilder) Build() ldmodel.FeatureFlag {
	f := b.flag
	ldmodel.PreprocessFlag(&f)
	return f
}

// AddPrerequisite adds a flag prerequisite.
func (b *FlagBuilder) AddPrerequisite(key string, variationIndex int) *FlagBuilder {
	b.flag.Prerequisites = append(b.flag.Prerequisites, ldmodel.Prerequisite{Key: key, Variation: variationIndex})
	return b
}

// AddRule adds a flag rule.
func (b *FlagBuilder) AddRule(r *RuleBuilder) *FlagBuilder {
	b.flag.Rules = append(b.flag.Rules, r.Build())
	return b
}

// AddTarget adds a user target set.
func (b *FlagBuilder) AddTarget(variationIndex int, keys ...string) *FlagBuilder {
	b.flag.Targets = append(b.flag.Targets, ldmodel.Target{Values: keys, Variation: variationIndex})
	return b
}

// ClientSide sets the flag's ClientSide property.
func (b *FlagBuilder) ClientSide(value bool) *FlagBuilder {
	b.flag.ClientSide = value
	return b
}

// DebugEventsUntilDate sets the flag's DebugEventsUntilDate property.
func (b *FlagBuilder) DebugEventsUntilDate(t ldtime.UnixMillisecondTime) *FlagBuilder {
	if t == 0 {
		b.flag.DebugEventsUntilDate = nil
	} else {
		b.flag.DebugEventsUntilDate = &t
	}
	return b
}

// Deleted sets the flag's Deleted property.
func (b *FlagBuilder) Deleted(value bool) *FlagBuilder {
	b.flag.Deleted = value
	return b
}

// Fallthrough sets the flag's Fallthrough property.
func (b *FlagBuilder) Fallthrough(vr ldmodel.VariationOrRollout) *FlagBuilder {
	b.flag.Fallthrough = vr
	return b
}

// FallthroughVariation sets the flag's Fallthrough property to a fixed variation.
func (b *FlagBuilder) FallthroughVariation(variationIndex int) *FlagBuilder {
	return b.Fallthrough(Variation(variationIndex))
}

// OffVariation sets the flag's OffVariation property.
func (b *FlagBuilder) OffVariation(variationIndex int) *FlagBuilder {
	if variationIndex == NoVariation {
		b.flag.OffVariation = nil
	} else {
		b.flag.OffVariation = &variationIndex
	}
	return b
}

// On sets the flag's On property.
func (b *FlagBuilder) On(value bool) *FlagBuilder {
	b.flag.On = value
	return b
}

// Salt sets the flag's Salt property.
func (b *FlagBuilder) Salt(value string) *FlagBuilder {
	b.flag.Salt = value
	return b
}

// SingleVariation configures the flag to have only one variation value which it always returns.
func (b *FlagBuilder) SingleVariation(value ldvalue.Value) *FlagBuilder {
	return b.Variations(value).OffVariation(0).On(false)
}

// TrackEvents sets the flag's TrackEvents property.
func (b *FlagBuilder) TrackEvents(value bool) *FlagBuilder {
	b.flag.TrackEvents = value
	return b
}

// TrackEventsFallthrough sets the flag's TrackEventsFallthrough property.
func (b *FlagBuilder) TrackEventsFallthrough(value bool) *FlagBuilder {
	b.flag.TrackEventsFallthrough = value
	return b
}

// Variations sets the flag's list of variation values.
func (b *FlagBuilder) Variations(values ...ldvalue.Value) *FlagBuilder {
	b.flag.Variations = values
	return b
}

// Version sets the flag's Version property.
func (b *FlagBuilder) Version(value int) *FlagBuilder {
	b.flag.Version = value
	return b
}

// NewRuleBuilder creates a RuleBuilder.
func NewRuleBuilder() *RuleBuilder {
	return &RuleBuilder{}
}

// Build returns the configured FlagRule.
func (b *RuleBuilder) Build() ldmodel.FlagRule {
	return b.rule
}

// Clauses sets the rule's list of clauses.
func (b *RuleBuilder) Clauses(clauses ...ldmodel.Clause) *RuleBuilder {
	b.rule.Clauses = clauses
	return b
}

// ID sets the rule's ID property.
func (b *RuleBuilder) ID(id string) *RuleBuilder {
	b.rule.ID = id
	return b
}

// TrackEvents sets the rule's TrackEvents property.
func (b *RuleBuilder) TrackEvents(value bool) *RuleBuilder {
	b.rule.TrackEvents = value
	return b
}

// Variation sets the rule to use a fixed variation.
func (b *RuleBuilder) Variation(variationIndex int) *RuleBuilder {
	return b.VariationOrRollout(Variation(variationIndex))
}

// VariationOrRollout sets the rule to use either a variation or a percentage rollout.
func (b *RuleBuilder) VariationOrRollout(vr ldmodel.VariationOrRollout) *RuleBuilder {
	b.rule.VariationOrRollout = vr
	return b
}

// Clause constructs a basic Clause.
func Clause(attr lduser.UserAttribute, op ldmodel.Operator, values ...ldvalue.Value) ldmodel.Clause {
	return ldmodel.Clause{Attribute: attr, Op: op, Values: values}
}

// Negate returns the same Clause with the Negated property set to true.
func Negate(c ldmodel.Clause) ldmodel.Clause {
	c.Negate = true
	return c
}

// SegmentMatchClause constructs a Clause that uses the segmentMatch operator.
func SegmentMatchClause(segmentKeys ...string) ldmodel.Clause {
	clause := ldmodel.Clause{Op: ldmodel.OperatorSegmentMatch}
	for _, key := range segmentKeys {
		clause.Values = append(clause.Values, ldvalue.String(key))
	}
	return clause
}
