package ldclient

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// EvalReasonKind defines the possible values of the Kind property of EvaluationReason.
type EvalReasonKind string

const (
	// EvalReasonOff indicates that the flag was off and therefore returned its configured off value.
	EvalReasonOff EvalReasonKind = "OFF"
	// EvalReasonTargetMatch indicates that the user key was specifically targeted for this flag.
	EvalReasonTargetMatch EvalReasonKind = "TARGET_MATCH"
	// EvalReasonRuleMatch indicates that the user matched one of the flag's rules.
	EvalReasonRuleMatch EvalReasonKind = "RULE_MATCH"
	// EvalReasonPrerequisiteFailed indicates that the flag was considered off because it had at
	// least one prerequisite flag that either was off or did not return the desired variation.
	EvalReasonPrerequisiteFailed EvalReasonKind = "PREREQUISITE_FAILED"
	// EvalReasonFallthrough indicates that the flag was on but the user did not match any targets
	// or rules.
	EvalReasonFallthrough EvalReasonKind = "FALLTHROUGH"
	// EvalReasonError indicates that the flag could not be evaluated, e.g. because it does not
	// exist or due to an unexpected error. In this case the result value will be the default value
	// that the caller passed to the client.
	EvalReasonError EvalReasonKind = "ERROR"
)

// EvalErrorKind defines the possible values of the ErrorKind property of EvaluationReason.
type EvalErrorKind string

const (
	// EvalErrorClientNotReady indicates that the caller tried to evaluate a flag before the client
	// had successfully initialized.
	EvalErrorClientNotReady EvalErrorKind = "CLIENT_NOT_READY"
	// EvalErrorFlagNotFound indicates that the caller provided a flag key that did not match any
	// known flag.
	EvalErrorFlagNotFound EvalErrorKind = "FLAG_NOT_FOUND"
	// EvalErrorMalformedFlag indicates that there was an internal inconsistency in the flag data,
	// e.g. a rule specified a nonexistent variation.
	EvalErrorMalformedFlag EvalErrorKind = "MALFORMED_FLAG"
	// EvalErrorUserNotSpecified indicates that the caller passed a user without a key for the user
	// parameter.
	EvalErrorUserNotSpecified EvalErrorKind = "USER_NOT_SPECIFIED"
	// EvalErrorWrongType indicates that the result value was not of the requested type, e.g. you
	// called BoolVariationDetail but the value was an integer.
	EvalErrorWrongType EvalErrorKind = "WRONG_TYPE"
	// EvalErrorException indicates that an unexpected error stopped flag evaluation; check the
	// log for details.
	EvalErrorException EvalErrorKind = "EXCEPTION"
)

// EvaluationReason describes the reason that a flag evaluation producted a particular value.
// Specific kinds of reasons have their own types that implement this interface.
type EvaluationReason interface {
	// GetKind describes the general category of the reason.
	GetKind() EvalReasonKind
}

type evaluationReasonBase struct {
	// Kind describes the general category of the reason.
	Kind EvalReasonKind `json:"kind"`
}

func (r evaluationReasonBase) GetKind() EvalReasonKind {
	return r.Kind
}

// EvaluationReasonOff means that the flag was off and therefore returned its configured off value.
type EvaluationReasonOff struct {
	evaluationReasonBase
}

var evalReasonOffInstance = EvaluationReasonOff{
	evaluationReasonBase: evaluationReasonBase{Kind: EvalReasonOff},
}

func (r EvaluationReasonOff) String() string {
	return string(r.GetKind())
}

// EvaluationReasonTargetMatch means that the user key was specifically targeted for this flag.
type EvaluationReasonTargetMatch struct {
	evaluationReasonBase
}

var evalReasonTargetMatchInstance = EvaluationReasonTargetMatch{
	evaluationReasonBase: evaluationReasonBase{Kind: EvalReasonTargetMatch},
}

func (r EvaluationReasonTargetMatch) String() string {
	return string(r.GetKind())
}

// EvaluationReasonRuleMatch means that the user matched one of the flag's rules.
type EvaluationReasonRuleMatch struct {
	evaluationReasonBase
	// RuleIndex is the index of the rule that was matched (0 being the first).
	RuleIndex int `json:"ruleIndex"`
	// RuleID is the unique identifier of the rule that was matched.
	RuleID string `json:"ruleId"`
}

func newEvalReasonRuleMatch(ruleIndex int, ruleID string) EvaluationReasonRuleMatch {
	return EvaluationReasonRuleMatch{
		evaluationReasonBase: evaluationReasonBase{Kind: EvalReasonRuleMatch},
		RuleIndex:            ruleIndex,
		RuleID:               ruleID,
	}
}

func (r EvaluationReasonRuleMatch) String() string {
	return fmt.Sprintf("%s(%d,%s)", r.GetKind(), r.RuleIndex, r.RuleID)
}

// EvaluationReasonPrerequisiteFailed means that the flag was considered off because it had at
// least one prerequisite flag that either was off or did not return the desired variation.
type EvaluationReasonPrerequisiteFailed struct {
	evaluationReasonBase
	// PrerequisiteKey is the flag key of the prerequisite that failed.
	PrerequisiteKey string `json:"prerequisiteKey"`
}

func newEvalReasonPrerequisiteFailed(prereqKey string) EvaluationReasonPrerequisiteFailed {
	return EvaluationReasonPrerequisiteFailed{
		evaluationReasonBase: evaluationReasonBase{Kind: EvalReasonPrerequisiteFailed},
		PrerequisiteKey:      prereqKey,
	}
}

func (r EvaluationReasonPrerequisiteFailed) String() string {
	return fmt.Sprintf("%s(%s)", r.GetKind(), r.PrerequisiteKey)
}

// EvaluationReasonFallthrough means that the flag was on but the user did not match any targets
// or rules.
type EvaluationReasonFallthrough struct {
	evaluationReasonBase
}

var evalReasonFallthroughInstance = EvaluationReasonFallthrough{
	evaluationReasonBase: evaluationReasonBase{Kind: EvalReasonFallthrough},
}

func (r EvaluationReasonFallthrough) String() string {
	return string(r.GetKind())
}

// EvaluationReasonError means that the flag could not be evaluated, e.g. because it does not
// exist or due to an unexpected error.
type EvaluationReasonError struct {
	evaluationReasonBase
	// ErrorKind describes the type of error.
	ErrorKind EvalErrorKind `json:"errorKind"`
}

func newEvalReasonError(kind EvalErrorKind) EvaluationReasonError {
	return EvaluationReasonError{
		evaluationReasonBase: evaluationReasonBase{Kind: EvalReasonError},
		ErrorKind:            kind,
	}
}

func (r EvaluationReasonError) String() string {
	return fmt.Sprintf("%s(%s)", r.GetKind(), r.ErrorKind)
}

// EvaluationDetail is an object returned by LDClient.VariationDetail, combining the result of a
// flag evaluation with an explanation of how it was calculated.
type EvaluationDetail struct {
	// Value is the result of the flag evaluation. This will be either one of the flag's variations or
	// the default value that was passed to the Variation method.
	Value interface{}
	// VariationIndex is the index of the returned value within the flag's list of variations, e.g.
	// 0 for the first variation - or nil if the default value was returned.
	VariationIndex *int
	// Reason is an EvaluationReason object describing the main factor that influenced the flag
	// evaluation value.
	Reason EvaluationReason
}

// IsDefaultValue returns true if the result of the evaluation was the default value.
func (d EvaluationDetail) IsDefaultValue() bool {
	return d.VariationIndex == nil
}

// EvaluationReasonContainer is used internally in cases where LaunchDarkly needs to unnmarshal
// an EvaluationReason value from JSON. This is necessary because UnmarshalJSON cannot be
// implemented for interfaces.
type EvaluationReasonContainer struct {
	Reason EvaluationReason
}

// MarshalJSON implements custom JSON serialization for EvaluationReasonContainer.
func (c EvaluationReasonContainer) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(c.Reason)
	return data, err
}

// UnmarshalJSON implements custom JSON deserialization for EvaluationReasonContainer.
func (c *EvaluationReasonContainer) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	var kindOnly struct {
		Kind EvalReasonKind `json:"kind"`
	}
	if err := json.Unmarshal(data, &kindOnly); err != nil {
		return err
	}
	switch kindOnly.Kind {
	case EvalReasonOff:
		c.Reason = evalReasonOffInstance
	case EvalReasonFallthrough:
		c.Reason = evalReasonFallthroughInstance
	case EvalReasonTargetMatch:
		c.Reason = evalReasonTargetMatchInstance
	case EvalReasonRuleMatch:
		var r EvaluationReasonRuleMatch
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		c.Reason = r
	case EvalReasonPrerequisiteFailed:
		var r EvaluationReasonPrerequisiteFailed
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		c.Reason = r
	case EvalReasonError:
		var r EvaluationReasonError
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		c.Reason = r
	default:
		return fmt.Errorf("Unknown evaluation reason kind: %s", kindOnly.Kind)
	}
	return nil
}

// Explanation is an obsolete type that is used by the deprecated EvaluateExplain method.
//
// Deprecated: Use the VariationDetail methods and the EvaluationDetail type instead.
type Explanation struct {
	Kind                string `json:"kind" bson:"kind"`
	*Target             `json:"target,omitempty"`
	*Rule               `json:"rule,omitempty"`
	*Prerequisite       `json:"prerequisite,omitempty"`
	*VariationOrRollout `json:"fallthrough,omitempty"`
}

// BEGIN DEPRECATED SECTION
// This code is only used to support the deprecated EvaluateExplain method, which requires us to
// convert our current EvaluationReason data into the obsolete Explanation type (which includes
// pointers to objects within the flag data model).

type deprecatedExplanationConversion interface {
	getOldExplanation(flag FeatureFlag, user User) Explanation
}

func (r EvaluationReasonOff) getOldExplanation(flag FeatureFlag, user User) Explanation {
	return Explanation{}
}

func (r EvaluationReasonFallthrough) getOldExplanation(flag FeatureFlag, user User) Explanation {
	return Explanation{}
}

func (r EvaluationReasonTargetMatch) getOldExplanation(flag FeatureFlag, user User) Explanation {
	var ret = Explanation{Kind: "target"}
	for _, target := range flag.Targets {
		for _, value := range target.Values {
			if value == *user.Key {
				ret.Target = &target
				return ret
			}
		}
	}
	return ret
}

func (r EvaluationReasonRuleMatch) getOldExplanation(flag FeatureFlag, user User) Explanation {
	var ret = Explanation{Kind: "rule"}
	if r.RuleIndex < len(flag.Rules) {
		rule := flag.Rules[r.RuleIndex]
		ret.Rule = &rule
	}
	return ret
}

func (r EvaluationReasonPrerequisiteFailed) getOldExplanation(flag FeatureFlag, user User) Explanation {
	var ret = Explanation{Kind: "prerequisite"}
	for _, prereq := range flag.Prerequisites {
		if prereq.Key == r.PrerequisiteKey {
			ret.Prerequisite = &prereq
			break
		}
	}
	return ret
}

func (r EvaluationReasonError) getOldExplanation(flag FeatureFlag, user User) Explanation {
	return Explanation{Kind: "error"}
}

// END DEPRECATED SECTION
