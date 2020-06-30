package ldmodel

import (
	"regexp"
	"time"

	"github.com/blang/semver"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
)

type targetPreprocessedData struct {
	valuesMap map[string]bool
}

type segmentPreprocessedData struct {
	includeMap map[string]bool
	excludeMap map[string]bool
}

type clausePreprocessedData struct {
	values    []clausePreprocessedValue
	valuesMap map[jsonPrimitiveValueKey]bool
}

type clausePreprocessedValue struct {
	computed     bool
	valid        bool
	parsedRegexp *regexp.Regexp // used for OperatorMatches
	parsedTime   time.Time      // used for OperatorAfter, OperatorBefore
	parsedSemver semver.Version // used for OperatorSemVerEqual, etc.
}

type jsonPrimitiveValueKey struct {
	valueType    ldvalue.ValueType
	booleanValue bool
	numberValue  float64
	stringValue  string
}

func (j jsonPrimitiveValueKey) isValid() bool {
	return j.valueType != ldvalue.NullType
}

// PreprocessFlag precomputes internal data structures based on the flag configuration, to speed up
// evaluations.
//
// This is called once after a flag is deserialized from JSON, or is created with ldbuilders. If you
// construct a flag by some other means, you should call PreprocessFlag exactly once before making it
// available to any other code. The method is not safe for concurrent access across goroutines.
func PreprocessFlag(f *FeatureFlag) {
	for i, t := range f.Targets {
		f.Targets[i].preprocessed = preprocessTarget(t)
	}
	for i, r := range f.Rules {
		for j, c := range r.Clauses {
			f.Rules[i].Clauses[j].preprocessed = preprocessClause(c)
		}
	}
}

// PreprocessSegment precomputes internal data structures based on the segment configuration, to speed up
// evaluations.
//
// This is called once after a segment is deserialized from JSON, or is created with ldbuilders. If you
// construct a segment by some other means, you should call PreprocessSegment exactly once before making
// it available to any other code. The method is not safe for concurrent access across goroutines.
func PreprocessSegment(s *Segment) {
	p := segmentPreprocessedData{}
	if len(s.Included) > 0 {
		p.includeMap = make(map[string]bool, len(s.Included))
		for _, key := range s.Included {
			p.includeMap[key] = true
		}
	}
	if len(s.Excluded) > 0 {
		p.excludeMap = make(map[string]bool, len(s.Excluded))
		for _, key := range s.Excluded {
			p.excludeMap[key] = true
		}
	}
	s.preprocessed = p

	for i, r := range s.Rules {
		for j, c := range r.Clauses {
			s.Rules[i].Clauses[j].preprocessed = preprocessClause(c)
		}
	}
}

func preprocessTarget(t Target) targetPreprocessedData {
	ret := targetPreprocessedData{}
	if len(t.Values) > 0 {
		m := make(map[string]bool, len(t.Values))
		for _, v := range t.Values {
			m[v] = true
		}
		ret.valuesMap = m
	}
	return ret
}

func preprocessClause(c Clause) clausePreprocessedData {
	ret := clausePreprocessedData{}
	switch c.Op {
	case OperatorIn:
		// This is a special case where the clause is testing for an exact match against any of the
		// clause values. As long as the values are primitives, we can use them in a map key (map
		// keys just can't contain slices or maps), and we can convert this test from a linear search
		// to a map lookup.
		if len(c.Values) > 1 { // don't bother if it's empty or has a single value
			valid := true
			m := make(map[jsonPrimitiveValueKey]bool, len(c.Values))
			for _, v := range c.Values {
				if key := asPrimitiveValueKey(v); key.isValid() {
					m[key] = true
				} else {
					valid = false
					break
				}
			}
			if valid {
				ret.valuesMap = m
			}
		}
	case OperatorMatches:
		ret.values = preprocessValues(c.Values, func(v ldvalue.Value) clausePreprocessedValue {
			r, ok := parseRegexp(v)
			return clausePreprocessedValue{valid: ok, parsedRegexp: r}
		})
	case OperatorBefore, OperatorAfter:
		ret.values = preprocessValues(c.Values, func(v ldvalue.Value) clausePreprocessedValue {
			t, ok := parseDateTime(v)
			return clausePreprocessedValue{valid: ok, parsedTime: t}
		})
	case OperatorSemVerEqual, OperatorSemVerGreaterThan, OperatorSemVerLessThan:
		ret.values = preprocessValues(c.Values, func(v ldvalue.Value) clausePreprocessedValue {
			s, ok := parseSemVer(v)
			return clausePreprocessedValue{valid: ok, parsedSemver: s}
		})
	default:
	}
	return ret
}

func asPrimitiveValueKey(v ldvalue.Value) jsonPrimitiveValueKey {
	switch v.Type() {
	case ldvalue.BoolType:
		return jsonPrimitiveValueKey{valueType: ldvalue.BoolType, booleanValue: v.BoolValue()}
	case ldvalue.NumberType:
		return jsonPrimitiveValueKey{valueType: ldvalue.NumberType, numberValue: v.Float64Value()}
	case ldvalue.StringType:
		return jsonPrimitiveValueKey{valueType: ldvalue.StringType, stringValue: v.StringValue()}
	default:
		return jsonPrimitiveValueKey{}
	}
}

func preprocessValues(
	valuesIn []ldvalue.Value,
	fn func(ldvalue.Value) clausePreprocessedValue,
) []clausePreprocessedValue {
	ret := make([]clausePreprocessedValue, len(valuesIn))
	for i, v := range valuesIn {
		p := fn(v)
		p.computed = true
		ret[i] = p
	}
	return ret
}
