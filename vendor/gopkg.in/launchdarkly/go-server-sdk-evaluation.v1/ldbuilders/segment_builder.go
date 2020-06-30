package ldbuilders

import (
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
)

// SegmentBuilder provides a builder pattern for Segment.
type SegmentBuilder struct {
	segment ldmodel.Segment
}

// SegmentRuleBuilder provides a builder pattern for SegmentRule.
type SegmentRuleBuilder struct {
	rule ldmodel.SegmentRule
}

// NewSegmentBuilder creates a SegmentBuilder.
func NewSegmentBuilder(key string) *SegmentBuilder {
	return &SegmentBuilder{ldmodel.Segment{Key: key}}
}

// Build returns the configured Segment.
func (b *SegmentBuilder) Build() ldmodel.Segment {
	s := b.segment
	ldmodel.PreprocessSegment(&s)
	return s
}

// AddRule adds a rule to the segment.
func (b *SegmentBuilder) AddRule(r *SegmentRuleBuilder) *SegmentBuilder {
	b.segment.Rules = append(b.segment.Rules, r.Build())
	return b
}

// Excluded sets the segment's Excluded list.
func (b *SegmentBuilder) Excluded(keys ...string) *SegmentBuilder {
	b.segment.Excluded = keys
	return b
}

// Included sets the segment's Included list.
func (b *SegmentBuilder) Included(keys ...string) *SegmentBuilder {
	b.segment.Included = keys
	return b
}

// Version sets the segment's Version property.
func (b *SegmentBuilder) Version(value int) *SegmentBuilder {
	b.segment.Version = value
	return b
}

// Salt sets the segment's Salt property.
func (b *SegmentBuilder) Salt(value string) *SegmentBuilder {
	b.segment.Salt = value
	return b
}

// NewSegmentRuleBuilder creates a SegmentRuleBuilder.
func NewSegmentRuleBuilder() *SegmentRuleBuilder {
	return &SegmentRuleBuilder{}
}

// Build returns the configured SegmentRule.
func (b *SegmentRuleBuilder) Build() ldmodel.SegmentRule {
	return b.rule
}

// BucketBy sets the rule's BucketBy property.
func (b *SegmentRuleBuilder) BucketBy(attr lduser.UserAttribute) *SegmentRuleBuilder {
	if attr == "" {
		b.rule.BucketBy = nil
	} else {
		b.rule.BucketBy = &attr
	}
	return b
}

// Clauses sets the rule's list of clauses.
func (b *SegmentRuleBuilder) Clauses(clauses ...ldmodel.Clause) *SegmentRuleBuilder {
	b.rule.Clauses = clauses
	return b
}

// ID sets the rule's ID property.
func (b *SegmentRuleBuilder) ID(id string) *SegmentRuleBuilder {
	b.rule.ID = id
	return b
}

// Weight sets the rule's Weight property.
func (b *SegmentRuleBuilder) Weight(value int) *SegmentRuleBuilder {
	if value <= 0 {
		b.rule.Weight = nil
	} else {
		b.rule.Weight = &value
	}
	return b
}
