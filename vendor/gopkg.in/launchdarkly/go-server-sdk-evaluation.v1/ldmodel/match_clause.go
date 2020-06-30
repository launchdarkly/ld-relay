package ldmodel

import (
	"regexp"
	"strings"
	"time"

	"github.com/blang/semver"

	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
)

// ClauseMatchesUser return true if the user matches the conditions in this clause.
//
// This method cannot be used if the clause's Operation is OperatorSegmentMatch, since that involves
// pulling data from outside of the clause. In that case it will simply return false.
//
// This part of the flag evaluation logic is defined in ldmodel and exported, rather than being
// internal to Evaluator, as a compromise to allow for optimizations that require storing precomputed
// data in the model object. Exporting this function is preferable to exporting those internal
// implementation details.
//
// The clause and user are passed by reference for efficiency only; the function will not modify
// them. Passing a nil value will cause a panic.
func ClauseMatchesUser(c *Clause, user *lduser.User) bool {
	uValue := user.GetAttribute(c.Attribute)
	if uValue.IsNull() {
		// if the user attribute is null/missing, it's an automatic non-match - regardless of c.Negate
		return false
	}
	matchFn := operatorFn(c.Op)

	// If the user value is an array, see if the intersection is non-empty. If so, this clause matches
	if uValue.Type() == ldvalue.ArrayType {
		for i := 0; i < uValue.Count(); i++ {
			if matchAny(c.Op, matchFn, uValue.GetByIndex(i), c.Values, c.preprocessed) {
				return maybeNegate(c.Negate, true)
			}
		}
		return maybeNegate(c.Negate, false)
	}

	return maybeNegate(c.Negate, matchAny(c.Op, matchFn, uValue, c.Values, c.preprocessed))
}

func maybeNegate(negate, result bool) bool {
	if negate {
		return !result
	}
	return result
}

func matchAny(
	op Operator,
	fn opFn,
	value ldvalue.Value,
	values []ldvalue.Value,
	preprocessed clausePreprocessedData,
) bool {
	if op == OperatorIn && preprocessed.valuesMap != nil {
		if key := asPrimitiveValueKey(value); key.isValid() { // see preprocessClausee
			return preprocessed.valuesMap[key]
		}
	}
	preValues := preprocessed.values
	for i, v := range values {
		var p clausePreprocessedValue
		if preValues != nil {
			p = preValues[i] // this slice is always the same length as values
		}
		if fn(value, v, p) {
			return true
		}
	}
	return false
}

type opFn (func(userValue ldvalue.Value, clauseValue ldvalue.Value, preprocessed clausePreprocessedValue) bool)

var versionNumericComponentsRegex = regexp.MustCompile(`^\d+(\.\d+)?(\.\d+)?`) //nolint:gochecknoglobals

var allOps = map[Operator]opFn{ //nolint:gochecknoglobals
	OperatorIn:                 operatorInFn,
	OperatorEndsWith:           operatorEndsWithFn,
	OperatorStartsWith:         operatorStartsWithFn,
	OperatorMatches:            operatorMatchesFn,
	OperatorContains:           operatorContainsFn,
	OperatorLessThan:           operatorLessThanFn,
	OperatorLessThanOrEqual:    operatorLessThanOrEqualFn,
	OperatorGreaterThan:        operatorGreaterThanFn,
	OperatorGreaterThanOrEqual: operatorGreaterThanOrEqualFn,
	OperatorBefore:             operatorBeforeFn,
	OperatorAfter:              operatorAfterFn,
	OperatorSemVerEqual:        operatorSemVerEqualFn,
	OperatorSemVerLessThan:     operatorSemVerLessThanFn,
	OperatorSemVerGreaterThan:  operatorSemVerGreaterThanFn,
}

func operatorFn(operator Operator) opFn {
	if op, ok := allOps[operator]; ok {
		return op
	}
	return operatorNoneFn
}

func operatorInFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return uValue.Equal(cValue)
}

func stringOperator(uValue ldvalue.Value, cValue ldvalue.Value, fn func(string, string) bool) bool {
	if uValue.Type() == ldvalue.StringType && cValue.Type() == ldvalue.StringType {
		return fn(uValue.StringValue(), cValue.StringValue())
	}
	return false
}

func operatorStartsWithFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return stringOperator(uValue, cValue, strings.HasPrefix)
}

func operatorEndsWithFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return stringOperator(uValue, cValue, strings.HasSuffix)
}

func operatorMatchesFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	if preprocessed.computed {
		// we have already tried to compile the clause value as a regex
		if uValue.Type() != ldvalue.StringType || !preprocessed.valid {
			return false
		}
		return preprocessed.parsedRegexp.MatchString(uValue.StringValue())
	}
	// the clause did not get preprocessed, so we'll evaluate from scratch
	return stringOperator(uValue, cValue, func(u string, c string) bool {
		if matched, err := regexp.MatchString(c, u); err == nil {
			return matched
		}
		return false
	})
}

func operatorContainsFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return stringOperator(uValue, cValue, strings.Contains)
}

func numericOperator(uValue ldvalue.Value, cValue ldvalue.Value, fn func(float64, float64) bool) bool {
	if uValue.IsNumber() && cValue.IsNumber() {
		return fn(uValue.Float64Value(), cValue.Float64Value())
	}
	return false
}

func operatorLessThanFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return numericOperator(uValue, cValue, func(u float64, c float64) bool { return u < c })
}

func operatorLessThanOrEqualFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return numericOperator(uValue, cValue, func(u float64, c float64) bool { return u <= c })
}

func operatorGreaterThanFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return numericOperator(uValue, cValue, func(u float64, c float64) bool { return u > c })
}

func operatorGreaterThanOrEqualFn(
	uValue ldvalue.Value,
	cValue ldvalue.Value,
	preprocessed clausePreprocessedValue,
) bool {
	return numericOperator(uValue, cValue, func(u float64, c float64) bool { return u >= c })
}

func dateOperator(
	uValue ldvalue.Value,
	cValue ldvalue.Value,
	preprocessed clausePreprocessedValue,
	fn func(time.Time, time.Time) bool,
) bool {
	if preprocessed.computed {
		// we have already tried to compile the clause value as a date/time
		if preprocessed.valid {
			if uTime, ok := parseDateTime(uValue); ok {
				return fn(uTime, preprocessed.parsedTime)
			}
		}
		return false
	}
	// the clause did not get preprocessed, so we'll evaluate from scratch
	if uTime, ok := parseDateTime(uValue); ok {
		if cTime, ok := parseDateTime(cValue); ok {
			return fn(uTime, cTime)
		}
	}
	return false
}

func operatorBeforeFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return dateOperator(uValue, cValue, preprocessed, time.Time.Before)
}

func operatorAfterFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return dateOperator(uValue, cValue, preprocessed, time.Time.After)
}

func semVerOperator(
	uValue ldvalue.Value,
	cValue ldvalue.Value,
	preprocessed clausePreprocessedValue,
	fn func(semver.Version, semver.Version) bool,
) bool {
	if preprocessed.computed {
		// we have already tried to parse the clause value as a version
		if preprocessed.valid {
			if uVer, ok := parseSemVer(uValue); ok {
				return fn(uVer, preprocessed.parsedSemver)
			}
		}
		return false
	}
	// the clause did not get preprocessed, so we'll evaluate from scratch
	if u, ok := parseSemVer(uValue); ok {
		if c, ok := parseSemVer(cValue); ok {
			return fn(u, c)
		}
	}
	return false
}

func operatorSemVerEqualFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return semVerOperator(uValue, cValue, preprocessed, semver.Version.Equals)
}

func operatorSemVerLessThanFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return semVerOperator(uValue, cValue, preprocessed, semver.Version.LT)
}

func operatorSemVerGreaterThanFn(
	uValue ldvalue.Value,
	cValue ldvalue.Value,
	preprocessed clausePreprocessedValue,
) bool {
	return semVerOperator(uValue, cValue, preprocessed, semver.Version.GT)
}

func operatorNoneFn(uValue ldvalue.Value, cValue ldvalue.Value, preprocessed clausePreprocessedValue) bool {
	return false
}
