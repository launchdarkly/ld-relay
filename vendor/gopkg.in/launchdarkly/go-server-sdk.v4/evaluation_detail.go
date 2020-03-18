package ldclient

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/launchdarkly/go-sdk-common.v1/ldvalue"
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
//
// Note: In a future version, EvaluationReason will be a struct, and the currently existing
// structs implementing this interface (EvaluationReasonOff, etc.) will be removed. For
// compatibility, avoid referencing those types and use only the EvaluationReason methods.
type EvaluationReason interface {
	fmt.Stringer

	// GetKind describes the general category of the reason.
	GetKind() EvalReasonKind

	// GetRuleIndex provides the index of the rule that was matched (0 being the first), if
	// the Kind is EvalReasonRuleMatch. Otherwise it returns -1.
	GetRuleIndex() int

	// GetRuleID provides the unique identifier of the rule that was matched, if the Kind is
	// EvalReasonRuleMatch. Otherwise it returns an empty string. Unlike the rule index, this
	// identifier will not change if other rules are added or deleted.
	GetRuleID() string

	// GetPrerequisiteKey provides the flag key of the prerequisite that failed, if the Kind
	// is EvalReasonPrerequisiteFailed. Otherwise it returns an empty string.
	GetPrerequisiteKey() string

	// GetErrorKind describes the general category of the error, if the Kind is EvalReasonError.
	// Otherwise it returns an empty string.
	GetErrorKind() EvalErrorKind
}

type evaluationReasonBase struct {
	// Kind describes the general category of the reason.
	Kind EvalReasonKind `json:"kind"`
}

func (r evaluationReasonBase) GetKind() EvalReasonKind {
	return r.Kind
}

// EvaluationReasonOff means that the flag was off and therefore returned its configured off value.
//
// Deprecated: This type will be removed in a future version. Use the GetKind() method on
// EvaluationReason instead to test for EvalReasonOff.
type EvaluationReasonOff struct {
	evaluationReasonBase
}

var evalReasonOffInstance EvaluationReason = EvaluationReasonOff{
	evaluationReasonBase: evaluationReasonBase{Kind: EvalReasonOff},
}

func (r EvaluationReasonOff) String() string {
	return string(r.GetKind())
}

// GetRuleIndex for this type always returns -1.
func (r EvaluationReasonOff) GetRuleIndex() int {
	return -1
}

// GetRuleID for this type always returns an empty string.
func (r EvaluationReasonOff) GetRuleID() string {
	return ""
}

// GetPrerequisiteKey for this type always returns an empty string.
func (r EvaluationReasonOff) GetPrerequisiteKey() string {
	return ""
}

// GetErrorKind for this type always returns an empty string.
func (r EvaluationReasonOff) GetErrorKind() EvalErrorKind {
	return ""
}

// EvaluationReasonTargetMatch means that the user key was specifically targeted for this flag.
//
// Deprecated: This type will be removed in a future version. Use the GetKind() method on
// EvaluationReason instead to test for EvalReasonTargetMatch.
type EvaluationReasonTargetMatch struct {
	evaluationReasonBase
}

var evalReasonTargetMatchInstance EvaluationReason = EvaluationReasonTargetMatch{
	evaluationReasonBase: evaluationReasonBase{Kind: EvalReasonTargetMatch},
}

func (r EvaluationReasonTargetMatch) String() string {
	return string(r.GetKind())
}

// GetRuleIndex for this type always returns -1.
func (r EvaluationReasonTargetMatch) GetRuleIndex() int {
	return -1
}

// GetRuleID for this type always returns an empty string.
func (r EvaluationReasonTargetMatch) GetRuleID() string {
	return ""
}

// GetPrerequisiteKey for this type always returns an empty string.
func (r EvaluationReasonTargetMatch) GetPrerequisiteKey() string {
	return ""
}

// GetErrorKind for this type always returns an empty string.
func (r EvaluationReasonTargetMatch) GetErrorKind() EvalErrorKind {
	return ""
}

// EvaluationReasonRuleMatch means that the user matched one of the flag's rules.
//
// Deprecated: This type will be removed in a future version. Use the GetKind() method on
// EvaluationReason instead to test for EvalReasonRuleMatch, and use GetRuleIndex() or
// GetRuleID() instead of accessing the RuleIndex and RuleID fields directly.
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

// GetRuleIndex provides the index of the rule that was matched (0 being the first).
func (r EvaluationReasonRuleMatch) GetRuleIndex() int {
	return r.RuleIndex
}

// GetRuleID provides the unique identifier of the rule that was matched.
func (r EvaluationReasonRuleMatch) GetRuleID() string {
	return r.RuleID
}

// GetPrerequisiteKey for this type always returns an empty string.
func (r EvaluationReasonRuleMatch) GetPrerequisiteKey() string {
	return ""
}

// GetErrorKind for this type always returns an empty string.
func (r EvaluationReasonRuleMatch) GetErrorKind() EvalErrorKind {
	return ""
}

// EvaluationReasonPrerequisiteFailed means that the flag was considered off because it had at
// least one prerequisite flag that either was off or did not return the desired variation.
//
// Deprecated: This type will be removed in a future version. Use the GetKind() method on
// EvaluationReason instead to test for EvalReasonPrerequisiteFailed, and use
// GetPrerequisiteKey() instead of accessing the PrerequisiteKey field directly.
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

// GetRuleIndex for this type always returns -1.
func (r EvaluationReasonPrerequisiteFailed) GetRuleIndex() int {
	return -1
}

// GetRuleID for this type always returns an empty string.
func (r EvaluationReasonPrerequisiteFailed) GetRuleID() string {
	return ""
}

// GetPrerequisiteKey provides the flag key of the prerequisite that failed.
func (r EvaluationReasonPrerequisiteFailed) GetPrerequisiteKey() string {
	return r.PrerequisiteKey
}

// GetErrorKind for this type always returns an empty string.
func (r EvaluationReasonPrerequisiteFailed) GetErrorKind() EvalErrorKind {
	return ""
}

// EvaluationReasonFallthrough means that the flag was on but the user did not match any targets
// or rules.
//
// Deprecated: This type will be removed in a future version. Use the GetKind() method on
// EvaluationReason instead to test for EvalReasonFallthrough.
type EvaluationReasonFallthrough struct {
	evaluationReasonBase
}

var evalReasonFallthroughInstance = EvaluationReasonFallthrough{
	evaluationReasonBase: evaluationReasonBase{Kind: EvalReasonFallthrough},
}

func (r EvaluationReasonFallthrough) String() string {
	return string(r.GetKind())
}

// GetRuleIndex for this type always returns -1.
func (r EvaluationReasonFallthrough) GetRuleIndex() int {
	return -1
}

// GetRuleID for this type always returns an empty string.
func (r EvaluationReasonFallthrough) GetRuleID() string {
	return ""
}

// GetPrerequisiteKey for this type always returns an empty string.
func (r EvaluationReasonFallthrough) GetPrerequisiteKey() string {
	return ""
}

// GetErrorKind for this type always returns an empty string.
func (r EvaluationReasonFallthrough) GetErrorKind() EvalErrorKind {
	return ""
}

// EvaluationReasonError means that the flag could not be evaluated, e.g. because it does not
// exist or due to an unexpected error.
//
// Deprecated: This type will be removed in a future version. Use the GetKind() method on
// EvaluationReason instead to test for EvalReasonError, and use GetErrorKind() instead of
// accessing the ErrorKind field directly.
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

// GetRuleIndex for this type always returns -1.
func (r EvaluationReasonError) GetRuleIndex() int {
	return -1
}

// GetRuleID for this type always returns an empty string.
func (r EvaluationReasonError) GetRuleID() string {
	return ""
}

// GetPrerequisiteKey for this type always returns an empty string.
func (r EvaluationReasonError) GetPrerequisiteKey() string {
	return ""
}

// GetErrorKind describes the general category of the error.
func (r EvaluationReasonError) GetErrorKind() EvalErrorKind {
	return r.ErrorKind
}

func (r EvaluationReasonError) String() string {
	return fmt.Sprintf("%s(%s)", r.GetKind(), r.ErrorKind)
}

// EvaluationDetail is an object returned by LDClient.VariationDetail, combining the result of a
// flag evaluation with an explanation of how it was calculated.
type EvaluationDetail struct {
	// Value is the result of the flag evaluation. This will be either one of the flag's variations or
	// the default value that was passed to the Variation method.
	//
	// Deprecated: Use JSONValue instead. The Value property will be removed in a future version.
	Value interface{}
	// JSONValue is the result of the flag evaluation, represented with the ldvalue.Value type.
	// This is always the same value you would get by calling LDClient.JSONVariation(). You can
	// convert it to a bool, int, string, etc. using methods of ldvalue.Value.
	//
	// This property is preferred over EvaluationDetail.Value, because the interface{} type of Value
	// can expose a mutable data structure (slice or map) and accidentally modifying such a structure
	// could affect SDK behavior.
	JSONValue ldvalue.Value
	// VariationIndex is the index of the returned value within the flag's list of variations, e.g.
	// 0 for the first variation - or nil if the default value was returned.
	VariationIndex *int
	// Reason is an EvaluationReason object describing the main factor that influenced the flag
	// evaluation value.
	Reason EvaluationReason
}

// NewEvaluationDetail creates an EvaluationDetail, specifying all fields. The deprecated Value property is set
// to the same value that is wrapped by jsonValue.
func NewEvaluationDetail(jsonValue ldvalue.Value, variationIndex *int, reason EvaluationReason) EvaluationDetail {
	return EvaluationDetail{
		Value:          jsonValue.UnsafeArbitraryValue(), //nolint // allow deprecated usage
		JSONValue:      jsonValue,
		VariationIndex: variationIndex,
		Reason:         reason,
	}
}

// NewEvaluationError creates an EvaluationDetail describing an error. The deprecated Value property is set
// to the same value that is wrapped by jsonValue.
func NewEvaluationError(jsonValue ldvalue.Value, errorKind EvalErrorKind) EvaluationDetail {
	return EvaluationDetail{
		Value:     jsonValue.UnsafeArbitraryValue(), //nolint // allow deprecated usage
		JSONValue: jsonValue,
		Reason:    newEvalReasonError(errorKind),
	}
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
