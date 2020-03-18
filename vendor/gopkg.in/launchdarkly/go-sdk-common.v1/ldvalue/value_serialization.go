package ldvalue

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// This file contains methods for converting Value to and from JSON.

// JSONString returns the JSON representation of the value.
func (v Value) JSONString() string {
	// The following is somewhat redundant with json.Marshal, but it avoids the overhead of
	// converting between byte arrays and strings.
	switch v.valueType {
	case NullType:
		return "null"
	case BoolType:
		if v.boolValue {
			return "true"
		}
		return "false"
	case NumberType:
		if v.IsInt() {
			return strconv.Itoa(int(v.numberValue))
		}
		return strconv.FormatFloat(v.numberValue, 'f', -1, 64)
	}
	// For all other types, we rely on our custom marshaller.
	bytes, err := json.Marshal(v)
	if err != nil {
		// It shouldn't be possible for marshalling to fail, because Value should only contain
		// JSON-compatible types. However, UnsafeUserArbitraryValue and UnsafeArbitraryValue do
		// allow a badly behaved application to put an incompatible type into an array or map.
		// In that case we simply discard the value.
		return ""
	}
	return string(bytes)
}

// MarshalJSON converts the Value to its JSON representation.
//
// Note that the "omitempty" tag for a struct field will not cause an empty Value field to be
// omitted; it will be output as null. If you want to completely omit a JSON property when there
// is no value, it must be a pointer; use AsPointer().
func (v Value) MarshalJSON() ([]byte, error) {
	switch v.valueType {
	case NullType:
		return []byte("null"), nil
	case BoolType:
		if v.boolValue {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case NumberType:
		if v.IsInt() {
			return []byte(strconv.Itoa(int(v.numberValue))), nil
		}
		return []byte(strconv.FormatFloat(v.numberValue, 'f', -1, 64)), nil
	case StringType:
		return json.Marshal(v.stringValue)
	case ArrayType:
		if v.immutableArrayValue != nil {
			return json.Marshal(v.immutableArrayValue)
		}
		return json.Marshal(v.unsafeValueInstance)
	case ObjectType:
		if v.immutableObjectValue != nil {
			return json.Marshal(v.immutableObjectValue)
		}
		return json.Marshal(v.unsafeValueInstance)
	case RawType:
		if v.unsafeValueInstance != nil {
			if o, ok := v.unsafeValueInstance.(json.RawMessage); ok {
				return o, nil
			}
		}
		return []byte(v.stringValue), nil
	}
	return nil, errors.New("unknown data type") // should not be possible
}

// UnmarshalJSON parses a Value from JSON.
func (v *Value) UnmarshalJSON(data []byte) error {
	if len(data) == 0 { // should not be possible, the parser doesn't pass empty slices to UnmarshalJSON
		return errors.New("cannot parse empty data")
	}
	firstCh := data[0]
	switch firstCh {
	case 'n':
		// Note that since Go 1.5, comparing a string to string([]byte) is optimized so it
		// does not actually create a new string from the byte slice.
		if string(data) == "null" {
			*v = Null()
			return nil
		}
	case 't', 'f':
		if string(data) == "true" {
			*v = Bool(true)
			return nil
		}
		if string(data) == "false" {
			*v = Bool(false)
			return nil
		}
	case '"':
		var s string
		e := json.Unmarshal(data, &s)
		if e == nil {
			*v = String(s)
		}
		return e
	case '[':
		var a []Value
		e := json.Unmarshal(data, &a)
		if e == nil {
			*v = Value{valueType: ArrayType, immutableArrayValue: a}
		}
		return e
	case '{':
		var o map[string]Value
		e := json.Unmarshal(data, &o)
		if e == nil {
			*v = Value{valueType: ObjectType, immutableObjectValue: o}
		}
		return e
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9': // note, JSON does not allow a leading '.'
		var n float64
		e := json.Unmarshal(data, &n)
		if e == nil {
			*v = Value{valueType: NumberType, numberValue: n}
		}
		return e
	}
	return fmt.Errorf("unknown JSON token: %s", data)
}
