package ldclient

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"strconv"
)

const (
	longScale = float32(0xFFFFFFFFFFFFFFF)
	userKey   = "key"
)

// FeatureFlag describes an individual feature flag
type FeatureFlag struct {
	Key                  string             `json:"key" bson:"key"`
	Version              int                `json:"version" bson:"version"`
	On                   bool               `json:"on" bson:"on"`
	TrackEvents          bool               `json:"trackEvents" bson:"trackEvents"`
	Deleted              bool               `json:"deleted" bson:"deleted"`
	Prerequisites        []Prerequisite     `json:"prerequisites" bson:"prerequisites"`
	Salt                 string             `json:"salt" bson:"salt"`
	Sel                  string             `json:"sel" bson:"sel"`
	Targets              []Target           `json:"targets" bson:"targets"`
	Rules                []Rule             `json:"rules" bson:"rules"`
	Fallthrough          VariationOrRollout `json:"fallthrough" bson:"fallthrough"`
	OffVariation         *int               `json:"offVariation" bson:"offVariation"`
	Variations           []interface{}      `json:"variations" bson:"variations"`
	DebugEventsUntilDate *uint64            `json:"debugEventsUntilDate" bson:"debugEventsUntilDate"`
}

// GetKey returns the string key for the feature flag
func (f *FeatureFlag) GetKey() string {
	return f.Key
}

// GetVersion returns the version of a flag
func (f *FeatureFlag) GetVersion() int {
	return f.Version
}

// IsDeleted returns whether a flag has been deleted
func (f *FeatureFlag) IsDeleted() bool {
	return f.Deleted
}

// Clone returns a copy of a flag
func (f *FeatureFlag) Clone() VersionedData {
	f1 := *f
	return &f1
}

// FeatureFlagVersionedDataKind implements VersionedDataKind and provides methods to build storage engine for flags
type FeatureFlagVersionedDataKind struct{}

// GetNamespace returns the a unique namespace identifier for feature flag objects
func (fk FeatureFlagVersionedDataKind) GetNamespace() string {
	return "features"
}

// String returns the namespace
func (fk FeatureFlagVersionedDataKind) String() string {
	return fk.GetNamespace()
}

// GetDefaultItem returns a default feature flag representation
func (fk FeatureFlagVersionedDataKind) GetDefaultItem() interface{} {
	return &FeatureFlag{}
}

// MakeDeletedItem returns representation of a deleted flag
func (fk FeatureFlagVersionedDataKind) MakeDeletedItem(key string, version int) VersionedData {
	return &FeatureFlag{Key: key, Version: version, Deleted: true}
}

// Features is convenience variable to access an instance of FeatureFlagVersionedDataKind
var Features FeatureFlagVersionedDataKind

// Rule xpresses a set of AND-ed matching conditions for a user, along with either a fixed
// variation or a set of rollout percentages
type Rule struct {
	VariationOrRollout `bson:",inline"`
	Clauses            []Clause `json:"clauses" bson:"clauses"`
}

// VariationOrRollout contains either the fixed variation or percent rollout to serve.
// Invariant: one of the variation or rollout must be non-nil.
type VariationOrRollout struct {
	Variation *int     `json:"variation,omitempty" bson:"variation,omitempty"`
	Rollout   *Rollout `json:"rollout,omitempty" bson:"rollout,omitempty"`
}

// Rollout describes how users will be bucketed into variations during a percentage rollout
type Rollout struct {
	Variations []WeightedVariation `json:"variations" bson:"variations"`
	BucketBy   *string             `json:"bucketBy,omitempty" bson:"bucketBy,omitempty"`
}

// Clause describes an individual cluuse within a targeting rule
type Clause struct {
	Attribute string        `json:"attribute" bson:"attribute"`
	Op        Operator      `json:"op" bson:"op"`
	Values    []interface{} `json:"values" bson:"values"` // An array, interpreted as an OR of values
	Negate    bool          `json:"negate" bson:"negate"`
}

// WeightedVariation describes a fraction of users who will receive a specific variation
type WeightedVariation struct {
	Variation int `json:"variation" bson:"variation"`
	Weight    int `json:"weight" bson:"weight"` // Ranges from 0 to 100000
}

// Target describes a set of users who will receive a specific variation
type Target struct {
	Values    []string `json:"values" bson:"values"`
	Variation int      `json:"variation" bson:"variation"`
}

// Explanation is one of: target, rule, prerequisite that wasn't met, or fallthrough rollout/variation
type Explanation struct {
	Kind                string `json:"kind" bson:"kind"`
	*Target             `json:"target,omitempty"`
	*Rule               `json:"rule,omitempty"`
	*Prerequisite       `json:"prerequisite,omitempty"`
	*VariationOrRollout `json:"fallthrough,omitempty"`
}

// Prerequisite describes a requirement that another feature flag return a specific variation
type Prerequisite struct {
	Key       string `json:"key"`
	Variation int    `json:"variation"`
}

func bucketUser(user User, key, attr, salt string) float32 {
	uValue, pass := user.valueOf(attr)

	idHash, ok := bucketableStringValue(uValue)
	if pass || !ok {
		return 0
	}

	if user.Secondary != nil {
		idHash = idHash + "." + *user.Secondary
	}

	h := sha1.New()
	_, _ = io.WriteString(h, key+"."+salt+"."+idHash)
	hash := hex.EncodeToString(h.Sum(nil))[:15]

	intVal, _ := strconv.ParseInt(hash, 16, 64)

	bucket := float32(intVal) / longScale

	return bucket
}

func bucketableStringValue(uValue interface{}) (string, bool) {
	if s, ok := uValue.(string); ok {
		return s, true
	}
	if i, ok := uValue.(int); ok {
		return strconv.Itoa(i), true
	}
	return "", false
}

// EvalResult describes the value and variation index that are the result of flag evaluation.
// It also includes a list of any prerequisite flags that were evaluated to generate the evaluation.
type EvalResult struct {
	Value                     interface{}
	Variation                 *int
	Explanation               *Explanation
	PrerequisiteRequestEvents []FeatureRequestEvent //to be sent to LD
}

// Evaluate returns the variation selected for a user.
// It also contains a list of events generated during evaluation.
func (f FeatureFlag) Evaluate(user User, store FeatureStore) (interface{}, *int, []FeatureRequestEvent) {
	var prereqEvents []FeatureRequestEvent
	if f.On {
		evalResult, err := f.EvaluateExplain(user, store)
		prereqEvents = evalResult.PrerequisiteRequestEvents

		if err != nil {
			return nil, nil, prereqEvents
		}

		if evalResult.Value != nil {
			return evalResult.Value, evalResult.Variation, prereqEvents
		}
		// If the value is nil, but the error is not, fall through and use the off variation
	}

	if f.OffVariation != nil && *f.OffVariation < len(f.Variations) {
		value := f.Variations[*f.OffVariation]
		return value, f.OffVariation, prereqEvents
	}
	return nil, nil, prereqEvents
}

// EvaluateExplain returns the variation selected for a user along with a detailed explanation of which rule
// resulted in the selected variation.
func (f FeatureFlag) EvaluateExplain(user User, store FeatureStore) (*EvalResult, error) {
	if user.Key == nil {
		return nil, nil
	}
	events := make([]FeatureRequestEvent, 0)
	value, index, explanation, err := f.evaluateExplain(user, store, &events)

	return &EvalResult{
		Value:                     value,
		Variation:                 index,
		Explanation:               explanation,
		PrerequisiteRequestEvents: events,
	}, err
}

func (f FeatureFlag) evaluateExplain(user User, store FeatureStore, events *[]FeatureRequestEvent) (interface{}, *int, *Explanation, error) {
	var failedPrereq *Prerequisite
	for _, prereq := range f.Prerequisites {
		data, err := store.Get(Features, prereq.Key)
		if err != nil || data == nil {
			failedPrereq = &prereq
			break
		}
		prereqFeatureFlag, _ := data.(*FeatureFlag)
		if prereqFeatureFlag.On {
			prereqValue, prereqIndex, _, prereqErr := prereqFeatureFlag.evaluateExplain(user, store, events)
			if prereqErr != nil {
				failedPrereq = &prereq
			}

			*events = append(*events, NewFeatureRequestEvent(prereq.Key, prereqFeatureFlag, user, prereqIndex, prereqValue, nil, &f.Key))
			variation, verr := prereqFeatureFlag.getVariation(&prereq.Variation)
			if prereqValue == nil || verr != nil || prereqValue != variation {
				failedPrereq = &prereq
			}
		} else {
			failedPrereq = &prereq
		}
	}

	if failedPrereq != nil {
		explanation := Explanation{
			Kind:         "prerequisite",
			Prerequisite: failedPrereq,
		} //return the last prereq to fail

		return nil, nil, &explanation, nil
	}

	index, explanation := f.evaluateExplainIndex(store, user)
	variation, verr := f.getVariation(index)

	if verr != nil {
		return nil, index, explanation, verr
	}
	return variation, index, explanation, nil
}

func (f FeatureFlag) getVariation(index *int) (interface{}, error) {
	if index == nil {
		return nil, nil
	}
	if index == nil || *index >= len(f.Variations) {
		return nil, errors.New("Invalid variation index")
	}
	return f.Variations[*index], nil
}

func (f FeatureFlag) evaluateExplainIndex(store FeatureStore, user User) (*int, *Explanation) {
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
		if rule.matchesUser(store, user) {
			variation := rule.variationIndexForUser(user, f.Key, f.Salt)

			if variation == nil {
				return nil, nil
			}
			explanation := Explanation{Kind: "rule", Rule: &rule}
			return variation, &explanation
		}
	}

	variation := f.Fallthrough.variationIndexForUser(user, f.Key, f.Salt)

	if variation == nil {
		return nil, nil
	}
	explanation := Explanation{Kind: "fallthrough", VariationOrRollout: &f.Fallthrough}
	return variation, &explanation
}

func (r Rule) matchesUser(store FeatureStore, user User) bool {
	for _, clause := range r.Clauses {
		if !clause.matchesUser(store, user) {
			return false
		}
	}
	return true
}

func (c Clause) matchesUserNoSegments(user User) bool {
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

func (c Clause) matchesUser(store FeatureStore, user User) bool {
	// In the case of a segment match operator, we check if the user is in any of the segments,
	// and possibly negate
	if c.Op == OperatorSegmentMatch {
		for _, value := range c.Values {
			if vStr, ok := value.(string); ok {
				data, _ := store.Get(Segments, vStr)
				// If segment is not found or the store got an error, data will be nil and we'll just fall through
				// the next block. Unfortunately we have no access to a logger here so this failure is silent.
				if segment, segmentOk := data.(*Segment); segmentOk {
					if matches, _ := segment.ContainsUser(user); matches {
						return c.maybeNegate(true)
					}
				}
			}
		}
		return c.maybeNegate(false)
	}

	return c.matchesUserNoSegments(user)
}

func (c Clause) maybeNegate(b bool) bool {
	if c.Negate {
		return !b
	}
	return b
}

func matchAny(fn opFn, value interface{}, values []interface{}) bool {
	for _, v := range values {
		if fn(value, v) {
			return true
		}
	}
	return false
}

func (r VariationOrRollout) variationIndexForUser(user User, key, salt string) *int {
	if r.Variation != nil {
		return r.Variation
	}
	if r.Rollout == nil {
		return nil
	}

	bucketBy := userKey
	if r.Rollout.BucketBy != nil {
		bucketBy = *r.Rollout.BucketBy
	}

	var bucket = bucketUser(user, key, bucketBy, salt)
	var sum float32

	for _, wv := range r.Rollout.Variations {
		sum += float32(wv.Weight) / 100000.0
		if bucket < sum {
			return &wv.Variation
		}
	}

	return nil
}
