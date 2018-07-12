package ldclient

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"reflect"
	"strconv"
	"time"
)

// Feature describes a Legacy (v1) feature
type Feature struct {
	Name         *string      `json:"name"`
	Key          *string      `json:"key"`
	Kind         *string      `json:"kind"`
	Salt         *string      `json:"salt"`
	On           *bool        `json:"on"`
	Variations   *[]Variation `json:"variations"`
	CommitDate   *time.Time   `json:"commitDate"`
	CreationDate *time.Time   `json:"creationDate"`
	Version      int          `json:"version,omitempty"`
	Deleted      bool         `json:"deleted,omitempty"`
}

// TargetRule describes an individual targeting rule
type TargetRule struct {
	Attribute string        `json:"attribute"`
	Op        Operator      `json:"op"`
	Values    []interface{} `json:"values"`
}

// Variation describes what value to return for a user
type Variation struct {
	Value      interface{}  `json:"value"`
	Weight     int          `json:"weight"`
	Targets    []TargetRule `json:"targets"`
	UserTarget *TargetRule  `json:"userTarget,omitempty"`
}

// Evaluate returns the value of a feature for a specified user
func (f Feature) Evaluate(user User) (value interface{}, rulesPassed bool) {
	value, _, rulesPassed = f.EvaluateExplain(user)
	return
}

// EvaluateExplain returns the value of a feature for a specified user with an explanation of which rule was applied
func (f Feature) EvaluateExplain(user User) (value interface{}, targetMatch *TargetRule, rulesPassed bool) {

	if !*f.On {
		return nil, nil, true
	}

	param, passErr := f.paramForId(user)

	if passErr {
		return nil, nil, true
	}

	for _, variation := range *f.Variations {
		target := variation.matchUser(user)
		if target != nil {
			return variation.Value, target, false
		}
	}

	for _, variation := range *f.Variations {
		target := variation.matchTarget(user)
		if target != nil {
			return variation.Value, target, false
		}

	}

	var sum float32

	for _, variation := range *f.Variations {
		sum += float32(variation.Weight) / 100.0
		if param < sum {
			return variation.Value, nil, false
		}
	}

	return nil, nil, true
}

func (f Feature) paramForId(user User) (float32, bool) {
	if user.Key == nil {
		return 0, true // without a key, this rule should pass
	}
	idHash := *user.Key

	if user.Secondary != nil {
		idHash = idHash + "." + *user.Secondary
	}

	h := sha1.New()
	_, _ = io.WriteString(h, *f.Key+"."+*f.Salt+"."+idHash)
	hash := hex.EncodeToString(h.Sum(nil))[:15]

	intVal, _ := strconv.ParseInt(hash, 16, 64)

	param := float32(intVal) / longScale

	return param, false
}

func (target TargetRule) matchCustom(user User) bool {
	if user.Custom == nil {
		return false
	}
	var v interface{} = (*user.Custom)[target.Attribute]

	if v == nil {
		return false
	}

	val := reflect.ValueOf(v)

	if val.Kind() == reflect.Array || val.Kind() == reflect.Slice {
		for i := 0; i < val.Len(); i++ {
			if compareValues(val.Index(i).Interface(), target.Values) {
				return true
			}
		}
		return false
	}
	return compareValues(v, target.Values)
}

func compareValues(value interface{}, values []interface{}) bool {
	if value == "" {
		return false
	}
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}

func (target TargetRule) matchTarget(user User) bool {
	var uValue interface{}
	switch target.Attribute {
	case userKey:
		if user.Key != nil {
			uValue = *user.Key
		}
	case "ip":
		if user.Ip != nil {
			uValue = *user.Ip
		}
	case "country":
		if user.Country != nil {
			uValue = *user.Country
		}
	case "email":
		if user.Email != nil {
			uValue = *user.Email
		}
	case "firstName":
		if user.FirstName != nil {
			uValue = *user.FirstName
		}
	case "lastName":
		if user.LastName != nil {
			uValue = *user.LastName
		}
	case "avatar":
		if user.Avatar != nil {
			uValue = *user.Avatar
		}
	case "name":
		if user.Name != nil {
			uValue = *user.Name
		}
	case "anonymous":
		if user.Anonymous != nil {
			uValue = *user.Anonymous
		}
	default:
		return target.matchCustom(user)
	}
	return compareValues(uValue, target.Values)
}

func (variation Variation) matchTarget(user User) *TargetRule {
	for _, target := range variation.Targets {
		if variation.UserTarget != nil && target.Attribute == userKey {
			continue
		}
		if target.matchTarget(user) {
			return &target
		}
	}
	return nil
}

func (variation Variation) matchUser(user User) *TargetRule {
	if variation.UserTarget != nil && variation.UserTarget.matchTarget(user) {
		return variation.UserTarget
	}
	return nil
}
