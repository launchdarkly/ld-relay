package ldreason

import (
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
//
// This struct is immutable; its properties can be accessed only via getter methods.
type EvaluationReason struct {
	kind            EvalReasonKind
	ruleIndex       int
	ruleID          string
	prerequisiteKey string
	errorKind       EvalErrorKind
}

// String returns a concise string representation of the reason. Examples: "OFF", "ERROR(WRONG_TYPE)".
func (r EvaluationReason) String() string {
	switch r.kind {
	case EvalReasonRuleMatch:
		return fmt.Sprintf("%s(%d,%s)", r.kind, r.ruleIndex, r.ruleID)
	case EvalReasonPrerequisiteFailed:
		return fmt.Sprintf("%s(%s)", r.kind, r.prerequisiteKey)
	case EvalReasonError:
		return fmt.Sprintf("%s(%s)", r.kind, r.errorKind)
	default:
		return string(r.GetKind())
	}
}

// GetKind describes the general category of the reason.
func (r EvaluationReason) GetKind() EvalReasonKind {
	return r.kind
}

// GetRuleIndex provides the index of the rule that was matched (0 being the first), if
// the Kind is EvalReasonRuleMatch. Otherwise it returns -1.
func (r EvaluationReason) GetRuleIndex() int {
	if r.kind == EvalReasonRuleMatch {
		return r.ruleIndex
	}
	return -1
}

// GetRuleID provides the unique identifier of the rule that was matched, if the Kind is
// EvalReasonRuleMatch. Otherwise it returns an empty string. Unlike the rule index, this
// identifier will not change if other rules are added or deleted.
func (r EvaluationReason) GetRuleID() string {
	return r.ruleID
}

// GetPrerequisiteKey provides the flag key of the prerequisite that failed, if the Kind
// is EvalReasonPrerequisiteFailed. Otherwise it returns an empty string.
func (r EvaluationReason) GetPrerequisiteKey() string {
	return r.prerequisiteKey
}

// GetErrorKind describes the general category of the error, if the Kind is EvalReasonError.
// Otherwise it returns an empty string.
func (r EvaluationReason) GetErrorKind() EvalErrorKind {
	return r.errorKind
}

// NewEvalReasonOff returns an EvaluationReason whose Kind is EvalReasonOff.
func NewEvalReasonOff() EvaluationReason {
	return EvaluationReason{kind: EvalReasonOff}
}

// NewEvalReasonFallthrough returns an EvaluationReason whose Kind is EvalReasonFallthrough.
func NewEvalReasonFallthrough() EvaluationReason {
	return EvaluationReason{kind: EvalReasonFallthrough}
}

// NewEvalReasonTargetMatch returns an EvaluationReason whose Kind is EvalReasonTargetMatch.
func NewEvalReasonTargetMatch() EvaluationReason {
	return EvaluationReason{kind: EvalReasonTargetMatch}
}

// NewEvalReasonRuleMatch returns an EvaluationReason whose Kind is EvalReasonRuleMatch.
func NewEvalReasonRuleMatch(ruleIndex int, ruleID string) EvaluationReason {
	return EvaluationReason{kind: EvalReasonRuleMatch, ruleIndex: ruleIndex, ruleID: ruleID}
}

// NewEvalReasonPrerequisiteFailed returns an EvaluationReason whose Kind is EvalReasonPrerequisiteFailed.
func NewEvalReasonPrerequisiteFailed(prereqKey string) EvaluationReason {
	return EvaluationReason{kind: EvalReasonPrerequisiteFailed, prerequisiteKey: prereqKey}
}

// NewEvalReasonError returns an EvaluationReason whose Kind is EvalReasonError.
func NewEvalReasonError(errorKind EvalErrorKind) EvaluationReason {
	return EvaluationReason{kind: EvalReasonError, errorKind: errorKind}
}

type evaluationReasonForMarshaling struct {
	Kind            EvalReasonKind `json:"kind"`
	RuleIndex       *int           `json:"ruleIndex,omitempty"`
	RuleID          string         `json:"ruleId,omitempty"`
	PrerequisiteKey string         `json:"prerequisiteKey,omitempty"`
	ErrorKind       EvalErrorKind  `json:"errorKind,omitempty"`
}

// MarshalJSON implements custom JSON serialization for EvaluationReason.
func (r EvaluationReason) MarshalJSON() ([]byte, error) {
	if r.kind == "" {
		return []byte("null"), nil
	}
	erm := evaluationReasonForMarshaling{
		Kind:            r.kind,
		RuleID:          r.ruleID,
		PrerequisiteKey: r.prerequisiteKey,
		ErrorKind:       r.errorKind,
	}
	if r.kind == EvalReasonRuleMatch {
		erm.RuleIndex = &r.ruleIndex
	}
	return json.Marshal(erm)
}

// UnmarshalJSON implements custom JSON deserialization for EvaluationReason.
func (r *EvaluationReason) UnmarshalJSON(data []byte) error {
	var erm evaluationReasonForMarshaling
	if err := json.Unmarshal(data, &erm); err != nil {
		return nil
	}
	*r = EvaluationReason{
		kind:            erm.Kind,
		ruleID:          erm.RuleID,
		prerequisiteKey: erm.PrerequisiteKey,
		errorKind:       erm.ErrorKind,
	}
	if erm.RuleIndex != nil {
		r.ruleIndex = *erm.RuleIndex
	}
	return nil
}
