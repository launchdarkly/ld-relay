package ldclient

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"reflect"
	"strconv"
)

type FeatureFlag struct {
	Key          string        `json:"key" bson:"key"`
	Version      int           `json:"version" bson:"version"`
	On           bool          `json:"on" bson:"on"`
	Salt         string        `json:"salt" bson:"salt"`
	Sel          string        `json:"sel" bson:"sel"`
	Targets      []Target      `json:"targets" bson:"targets"`
	Rules        []Rule        `json:"rules" bson:"rules"`
	Fallthrough  Rule          `json:"fallthrough" bson:"fallthrough"`
	OffVariation *int          `json:"offVariation" bson:"offVariation"`
	Variations   []interface{} `json:"variations" bson:"variations"`
}

// Expresses a set of AND-ed matching conditions for a user, along with
// either the fixed variation or percent rollout to serve if the conditions
// match.
// Invariant: one of the variation or rollout must be non-nil.
type Rule struct {
	Clauses   []Clause `json:"clauses,omitempty" bson:"clauses,omitempty"`
	Variation *int     `json:"variation,omitempty" bson:"variation,omitempty"`
	Rollout   *Rollout `json:"rollout,omitempty" bson:"rollout,omitempty"`
}

type Rollout struct {
	Variations []WeightedVariation `json:"variations,omitempty" bson:"variations"`
	BucketBy   *string             `json:"bucketBy,omitempty" bson:"bucketBy,omitempty"`
}

type Clause struct {
	Attribute string        `json:"attribute" bson:"attribute"`
	Op        Operator      `json:"op" bson:"op"`
	Values    []interface{} `json:"values" bson:"values"` // An array, interpreted as an OR of values
	Negate    bool          `json:"negate" bson:"negate"`
}

type WeightedVariation struct {
	Variation int `json:"variation" bson:"variation"`
	Weight    int `json:"weight" bson:"weight"` // Ranges from 0 to 100000
}

type Target struct {
	Values    []string `json:"values" bson:"values"`
	Variation int      `json:"variation" bson:"variation"`
}

// An explanation is either a target or a rule
type Explanation struct {
	Kind string `json:"kind" bson:"kind"`
	*Target
	*Rule
}

func bucketUser(user User, key, attr, salt string) float32 {

	uValue, pass := user.valueOf(attr)

	if idHash, ok := uValue.(string); pass || !ok {
		return 0
	} else {
		if user.Secondary != nil {
			idHash = idHash + "." + *user.Secondary
		}

		h := sha1.New()
		io.WriteString(h, key+"."+salt+"."+idHash)
		hash := hex.EncodeToString(h.Sum(nil))[:15]

		intVal, _ := strconv.ParseInt(hash, 16, 64)

		bucket := float32(intVal) / long_scale

		return bucket
	}
}

func (f FeatureFlag) EvaluateExplain(user User) (interface{}, *Explanation) {
	index, explanation := f.evaluateExplainIndex(user)

	if index == nil || *index >= len(f.Variations) {
		return nil, explanation
	} else {
		return f.Variations[*index], explanation
	}

}

func (f FeatureFlag) evaluateExplainIndex(user User) (*int, *Explanation) {
	// TODO: The toggle algorithm should check the kill switch. We won't check it here
	// so we can potentially compute an explanation even if the kill switch is hit
	if user.Key == nil {
		return f.OffVariation, nil
	}

	// Check to see if targets match
	for _, target := range f.Targets {
		for _, value := range target.Values {
			if value == *user.Key {
				explanation := Explanation{Kind: "target", Target: &target}
				return &target.Variation, &explanation
			}
		}
	}

	// Now walk through the rules and see if any match
	for _, rule := range f.Rules {
		if rule.matchesUser(user) {
			variation := rule.variationIndexForUser(user, f.Key, f.Salt)

			if variation == nil {
				return f.OffVariation, nil // TODO should this continue, or return the off variation?
			} else {
				explanation := Explanation{Kind: "rule", Rule: &rule}
				return variation, &explanation
			}
		}
	}

	// Walk through the fallthrough and see if it matches
	variation := f.Fallthrough.variationIndexForUser(user, f.Key, f.Salt)

	if variation == nil {
		return f.OffVariation, nil
	} else {
		explanation := Explanation{Kind: "fallthrough", Rule: &f.Fallthrough}
		return variation, &explanation
	}
}

func (r Rule) matchesUser(user User) bool {
	for _, clause := range r.Clauses {
		if !clause.matchesUser(user) {
			return false
		}
	}
	return true
}

func (c Clause) matchesUser(user User) bool {
	uValue, pass := user.valueOf(c.Attribute)

	if pass {
		return false
	}
	matchFn := operatorFn(c.Op)

	val := reflect.ValueOf(uValue)

	// If the user value is an array or slice,
	// see if the intersection is non-empty. If so,
	// this clause matches
	if val.Kind() == reflect.Array || val.Kind() == reflect.Slice {
		for i := 0; i < val.Len(); i++ {
			if matchAny(matchFn, val.Index(i).Interface(), c.Values) {
				return c.maybeNegate(true)
			}
		}
		return c.maybeNegate(false)
	}

	return c.maybeNegate(matchAny(matchFn, uValue, c.Values))
}

func (c Clause) maybeNegate(b bool) bool {
	if c.Negate {
		return !b
	} else {
		return b
	}
}

func matchAny(fn opFn, value interface{}, values []interface{}) bool {
	for _, v := range values {
		if fn(value, v) {
			return true
		}
	}
	return false
}

func (r Rule) variationIndexForUser(user User, key, salt string) *int {
	if r.Variation != nil {
		return r.Variation
	} else if r.Rollout != nil {
		bucketBy := "key"
		if r.Rollout.BucketBy != nil {
			bucketBy = *r.Rollout.BucketBy
		}

		var bucket = bucketUser(user, key, bucketBy, salt)
		var sum float32 = 0.0

		for _, wv := range r.Rollout.Variations {
			sum += float32(wv.Weight) / 100000.0
			if bucket < sum {
				return &wv.Variation
			}
		}

	}

	return nil
}
