package evaluation

import (
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldreason"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
)

type evaluator struct {
	dataProvider DataProvider
}

// NewEvaluator creates an Evaluator, specifying a DataProvider that it will use if it needs to
// query additional feature flags or user segments during an evaluation.
func NewEvaluator(dataProvider DataProvider) Evaluator {
	return &evaluator{dataProvider}
}

// Used internally to hold the parameters of an evaluation, to avoid repetitive parameter passing.
// Its methods use a pointer receiver for efficiency, even though it is allocated on the stack and
// its fields are never modified.
type evaluationScope struct {
	owner                         *evaluator
	flag                          *ldmodel.FeatureFlag
	user                          lduser.User
	prerequisiteFlagEventRecorder PrerequisiteFlagEventRecorder
}

// Implementation of the Evaluator interface.
func (e *evaluator) Evaluate(
	flag *ldmodel.FeatureFlag,
	user lduser.User,
	prerequisiteFlagEventRecorder PrerequisiteFlagEventRecorder,
) ldreason.EvaluationDetail {
	es := evaluationScope{e, flag, user, prerequisiteFlagEventRecorder}
	return es.evaluate()
}

func (es *evaluationScope) evaluate() ldreason.EvaluationDetail {
	if !es.flag.On {
		return es.getOffValue(ldreason.NewEvalReasonOff())
	}

	// Note that all of our internal methods operate on pointers (*User, *FeatureFlag, *Clause, etc.);
	// this is done to avoid the overhead of repeatedly copying these structs by value. We know that
	// the pointers cannot be nil, since the entry point is always Evaluate which does receive its
	// parameters by value; mutability is not a concern, since User is immutable and the evaluation
	// code will never modify anything in the data model. Taking the address of these structs will not
	// cause heap escaping because we are never *returning* pointers (and never passing them to
	// external code such as prerequisiteFlagEventRecorder).

	prereqErrorReason, ok := es.checkPrerequisites()
	if !ok {
		return es.getOffValue(prereqErrorReason)
	}

	key := es.user.GetKey()

	// Check to see if targets match
	for _, target := range es.flag.Targets {
		// Note, taking address of range variable here is OK because it's not used outside the loop
		if ldmodel.TargetContainsKey(&target, key) { //nolint:gosec // see comment above
			return es.getVariation(target.Variation, ldreason.NewEvalReasonTargetMatch())
		}
	}

	// Now walk through the rules and see if any match
	for ruleIndex, rule := range es.flag.Rules {
		// Note, taking address of range variable here is OK because it's not used outside the loop
		if es.ruleMatchesUser(&rule) { //nolint:gosec // see comment above
			reason := ldreason.NewEvalReasonRuleMatch(ruleIndex, rule.ID)
			return es.getValueForVariationOrRollout(rule.VariationOrRollout, reason)
		}
	}

	return es.getValueForVariationOrRollout(es.flag.Fallthrough, ldreason.NewEvalReasonFallthrough())
}

// Returns an empty reason if all prerequisites are OK, otherwise constructs an error reason that describes the failure
func (es *evaluationScope) checkPrerequisites() (ldreason.EvaluationReason, bool) {
	if len(es.flag.Prerequisites) == 0 {
		return ldreason.EvaluationReason{}, true
	}

	for _, prereq := range es.flag.Prerequisites {
		prereqFeatureFlag := es.owner.dataProvider.GetFeatureFlag(prereq.Key)
		if prereqFeatureFlag == nil {
			return ldreason.NewEvalReasonPrerequisiteFailed(prereq.Key), false
		}
		prereqOK := true

		prereqResult := es.owner.Evaluate(prereqFeatureFlag, es.user, es.prerequisiteFlagEventRecorder)
		if !prereqFeatureFlag.On || prereqResult.IsDefaultValue() || prereqResult.VariationIndex != prereq.Variation {
			// Note that if the prerequisite flag is off, we don't consider it a match no matter what its
			// off variation was. But we still need to evaluate it in order to generate an event.
			prereqOK = false
		}

		if es.prerequisiteFlagEventRecorder != nil {
			event := PrerequisiteFlagEvent{es.flag.Key, es.user, prereqFeatureFlag, prereqResult}
			es.prerequisiteFlagEventRecorder(event)
		}

		if !prereqOK {
			return ldreason.NewEvalReasonPrerequisiteFailed(prereq.Key), false
		}
	}
	return ldreason.EvaluationReason{}, true
}

func (es *evaluationScope) getVariation(index int, reason ldreason.EvaluationReason) ldreason.EvaluationDetail {
	if index < 0 || index >= len(es.flag.Variations) {
		return ldreason.NewEvaluationDetailForError(ldreason.EvalErrorMalformedFlag, ldvalue.Null())
	}
	return ldreason.NewEvaluationDetail(es.flag.Variations[index], index, reason)
}

func (es *evaluationScope) getOffValue(reason ldreason.EvaluationReason) ldreason.EvaluationDetail {
	if es.flag.OffVariation == nil {
		return ldreason.NewEvaluationDetail(ldvalue.Null(), -1, reason)
	}
	return es.getVariation(*es.flag.OffVariation, reason)
}

func (es *evaluationScope) getValueForVariationOrRollout(
	vr ldmodel.VariationOrRollout,
	reason ldreason.EvaluationReason,
) ldreason.EvaluationDetail {
	index := es.variationIndexForUser(vr, es.flag.Key, es.flag.Salt)
	if index < 0 {
		return ldreason.NewEvaluationDetailForError(ldreason.EvalErrorMalformedFlag, ldvalue.Null())
	}
	return es.getVariation(index, reason)
}

func (es *evaluationScope) ruleMatchesUser(rule *ldmodel.FlagRule) bool {
	// Note that rule is passed by reference only for efficiency; we do not modify it
	for _, clause := range rule.Clauses {
		// Note, taking address of range variable here is OK because it's not used outside the loop
		if !es.clauseMatchesUser(&clause) { //nolint:gosec // see comment above
			return false
		}
	}
	return true
}

func (es *evaluationScope) clauseMatchesUser(clause *ldmodel.Clause) bool {
	// Note that clause is passed by reference only for efficiency; we do not modify it
	// In the case of a segment match operator, we check if the user is in any of the segments,
	// and possibly negate
	if clause.Op == ldmodel.OperatorSegmentMatch {
		for _, value := range clause.Values {
			if value.Type() == ldvalue.StringType {
				if segment := es.owner.dataProvider.GetSegment(value.StringValue()); segment != nil {
					if matches, _ := es.segmentContainsUser(segment); matches {
						return !clause.Negate // match - true unless negated
					}
				}
			}
		}
		return clause.Negate // non-match - false unless negated
	}

	return ldmodel.ClauseMatchesUser(clause, &es.user)
}

func (es *evaluationScope) variationIndexForUser(r ldmodel.VariationOrRollout, key, salt string) int {
	if r.Variation != nil {
		return *r.Variation
	}
	if r.Rollout == nil {
		// This is an error (malformed flag); either Variation or Rollout must be non-nil.
		return -1
	}

	bucketBy := lduser.KeyAttribute
	if r.Rollout.BucketBy != nil {
		bucketBy = *r.Rollout.BucketBy
	}

	var bucket = es.bucketUser(key, bucketBy, salt)
	var sum float32

	if len(r.Rollout.Variations) == 0 {
		// This is an error (malformed flag); there must be at least one weighted variation.
		return -1
	}
	for _, wv := range r.Rollout.Variations {
		sum += float32(wv.Weight) / 100000.0
		if bucket < sum {
			return wv.Variation
		}
	}

	// The user's bucket value was greater than or equal to the end of the last bucket. This could happen due
	// to a rounding error, or due to the fact that we are scaling to 100000 rather than 99999, or the flag
	// data could contain buckets that don't actually add up to 100000. Rather than returning an error in
	// this case (or changing the scaling, which would potentially change the results for *all* users), we
	// will simply put the user in the last bucket.
	return r.Rollout.Variations[len(r.Rollout.Variations)-1].Variation
}
