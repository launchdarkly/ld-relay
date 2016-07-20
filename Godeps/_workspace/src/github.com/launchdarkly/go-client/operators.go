package ldclient

import (
	"regexp"
	"strings"
)

const (
	operatorIn         Operator = "in"
	operatorEndsWith   Operator = "endsWith"
	operatorStartsWith Operator = "startsWith"
	operatorMatches    Operator = "matches"
	operatorContains   Operator = "contains"
)

type opFn (func(interface{}, interface{}) bool)

var allOps = map[Operator]opFn{
	operatorIn:         operatorInFn,
	operatorEndsWith:   operatorEndsWithFn,
	operatorStartsWith: operatorStartsWithFn,
	operatorMatches:    operatorMatchesFn,
	operatorContains:   operatorContainsFn,
}

// Turn this into a static map
func operatorFn(operator Operator) opFn {
	if op, ok := allOps[operator]; ok {
		return op
	} else {
		return operatorNoneFn
	}
}

func operatorInFn(uValue interface{}, cValue interface{}) bool {
	return uValue == cValue
}

func operatorStartsWithFn(uValue interface{}, cValue interface{}) bool {
	if uStr, ok := uValue.(string); ok {
		if cStr, ok := cValue.(string); ok {
			return strings.HasPrefix(uStr, cStr)
		}
	}
	return false
}

func operatorEndsWithFn(uValue interface{}, cValue interface{}) bool {
	if uStr, ok := uValue.(string); ok {
		if cStr, ok := cValue.(string); ok {
			return strings.HasSuffix(uStr, cStr)
		}
	}
	return false
}

func operatorMatchesFn(uValue interface{}, cValue interface{}) bool {
	if uStr, ok := uValue.(string); ok {
		if pattern, ok := cValue.(string); ok {
			if matched, err := regexp.MatchString(pattern, uStr); err == nil {
				return matched
			} else {
				return false
			}
		}
	}
	return false
}

func operatorContainsFn(uValue interface{}, cValue interface{}) bool {
	if uStr, ok := uValue.(string); ok {
		if cStr, ok := cValue.(string); ok {
			return strings.Contains(uStr, cStr)
		}
	}
	return false
}

func operatorNoneFn(uValue interface{}, cValue interface{}) bool {
	return false
}
