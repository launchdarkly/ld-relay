package evaluation

import (
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
)

// SegmentExplanation describes a rule that determines whether a user was included in or excluded from a segment
type SegmentExplanation struct {
	Kind        string
	MatchedRule *ldmodel.SegmentRule
}

func (es *evaluationScope) segmentContainsUser(s *ldmodel.Segment) (bool, SegmentExplanation) {
	userKey := es.user.GetKey()

	// Check if the user is specifically included in or excluded from the segment by key
	if included, found := ldmodel.SegmentIncludesOrExcludesKey(s, userKey); found {
		if included {
			return true, SegmentExplanation{Kind: "included"}
		}
		return false, SegmentExplanation{Kind: "excluded"}
	}

	// Check if any of the segment rules match
	for _, rule := range s.Rules {
		// Note, taking address of range variable here is OK because it's not used outside the loop
		if es.segmentRuleMatchesUser(&rule, s.Key, s.Salt) { //nolint:gosec // see comment above
			reason := rule
			return true, SegmentExplanation{Kind: "rule", MatchedRule: &reason}
		}
	}

	return false, SegmentExplanation{}
}

func (es *evaluationScope) segmentRuleMatchesUser(r *ldmodel.SegmentRule, key, salt string) bool {
	// Note that r is passed by reference only for efficiency; we do not modify it
	for _, clause := range r.Clauses {
		c := clause
		if !ldmodel.ClauseMatchesUser(&c, &es.user) {
			return false
		}
	}

	// If the Weight is absent, this rule matches
	if r.Weight == nil {
		return true
	}

	// All of the clauses are met. Check to see if the user buckets in
	bucketBy := lduser.KeyAttribute
	if r.BucketBy != nil {
		bucketBy = *r.BucketBy
	}

	// Check whether the user buckets into the segment
	bucket := es.bucketUser(key, bucketBy, salt)
	weight := float32(*r.Weight) / 100000.0

	return bucket < weight
}
