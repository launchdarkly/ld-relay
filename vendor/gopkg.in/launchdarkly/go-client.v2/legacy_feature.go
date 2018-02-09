package ldclient

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"reflect"
	"strconv"
	"time"
)

// Legacy (v1) feature
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

type TargetRule struct {
	Attribute string        `json:"attribute"`
	Op        Operator      `json:"op"`
	Values    []interface{} `json:"values"`
}

type Variation struct {
	Value      interface{}  `json:"value"`
	Weight     int          `json:"weight"`
	Targets    []TargetRule `json:"targets"`
	UserTarget *TargetRule  `json:"userTarget,omitempty"`
}

func (f Feature) Evaluate(user User) (value interface{}, rulesPassed bool) {
	value, _, rulesPassed = f.EvaluateExplain(user)
	return
}

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

	var sum float32 = 0.0

	for _, variation := range *f.Variations {
		sum += float32(variation.Weight) / 100.0
		if param < sum {
			return variation.Value, nil, false
		}
	}

	return nil, nil, true
}

func (b Feature) paramForId(user User) (float32, bool) {
	var idHash string

	if user.Key != nil {
		idHash = *user.Key
	} else { // without a key, this rule should pass
		return 0, true
	}

	if user.Secondary != nil {
		idHash = idHash + "." + *user.Secondary
	}

	h := sha1.New()
	io.WriteString(h, *b.Key+"."+*b.Salt+"."+idHash)
	hash := hex.EncodeToString(h.Sum(nil))[:15]

	intVal, _ := strconv.ParseInt(hash, 16, 64)

	param := float32(intVal) / long_scale

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
	} else {
		return compareValues(v, target.Values)
	}
}

func compareValues(value interface{}, values []interface{}) bool {
	if value == "" {
		return false
	} else {
		for _, v := range values {
			if value == v {
				return true
			}
		}
	}
	return false
}

func (target TargetRule) matchTarget(user User) bool {
	var uValue interface{}
	if target.Attribute == "key" {
		if user.Key != nil {
			uValue = *user.Key
		}
	} else if target.Attribute == "ip" {
		if user.Ip != nil {
			uValue = *user.Ip
		}
	} else if target.Attribute == "country" {
		if user.Country != nil {
			uValue = *user.Country
		}
	} else if target.Attribute == "email" {
		if user.Email != nil {
			uValue = *user.Email
		}
	} else if target.Attribute == "firstName" {
		if user.FirstName != nil {
			uValue = *user.FirstName
		}
	} else if target.Attribute == "lastName" {
		if user.LastName != nil {
			uValue = *user.LastName
		}
	} else if target.Attribute == "avatar" {
		if user.Avatar != nil {
			uValue = *user.Avatar
		}
	} else if target.Attribute == "name" {
		if user.Name != nil {
			uValue = *user.Name
		}
	} else if target.Attribute == "anonymous" {
		if user.Anonymous != nil {
			uValue = *user.Anonymous
		}
	} else {
		if target.matchCustom(user) {
			return true
		} else {
			return false
		}
	}

	if compareValues(uValue, target.Values) {
		return true
	} else {
		return false
	}
}

func (variation Variation) matchTarget(user User) *TargetRule {
	for _, target := range variation.Targets {
		if variation.UserTarget != nil && target.Attribute == "key" {
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
