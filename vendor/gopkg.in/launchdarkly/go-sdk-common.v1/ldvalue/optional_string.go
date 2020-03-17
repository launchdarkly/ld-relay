package ldvalue

import (
	"encoding/json"
	"errors"
	"fmt"
)

// OptionalString represents a string that may or may not have a value. This is similar to using a
// string pointer to distinguish between an empty string and nil, but it is safer because it does
// not expose a pointer to any mutable value.
//
// Unlike Value, which can contain a value of any JSON-compatible type, OptionalString either
// contains a string or nothing. To create an instance with a string value, use NewOptionalString.
// There is no corresponding method for creating an instance with no value; simply use the empty
// literal OptionalString{}.
//
//     os1 := NewOptionalString("this has a value")
//     os2 := NewOptionalString("") // this has a value which is an empty string
//     os2 := OptionalString{} // this does not have a value
//
// This can also be used as a convenient way to construct a string pointer within an expression.
// For instance, this example causes myStringPointer to point to the string "x":
//
//     var myStringPointer *string = NewOptionalString("x").AsPointer()
type OptionalString struct {
	value    string
	hasValue bool
}

// NewOptionalString constructs an OptionalString that has a string value.
//
// There is no corresponding method for creating an OptionalString with no value; simply use
// the empty literal OptionalString{}.
func NewOptionalString(value string) OptionalString {
	return OptionalString{value: value, hasValue: true}
}

// NewOptionalStringFromPointer constructs an OptionalString from a string pointer. If the pointer
// is non-nil, then the OptionalString copies its value; otherwise the OptionalString is empty.
func NewOptionalStringFromPointer(valuePointer *string) OptionalString {
	if valuePointer == nil {
		return OptionalString{hasValue: false}
	}
	return OptionalString{value: *valuePointer, hasValue: true}
}

// IsDefined returns true if the OptionalString contains a string value, or false if it is empty.
func (o OptionalString) IsDefined() bool {
	return o.hasValue
}

// StringValue returns the OptionalString's value, or an empty string if it has no value.
func (o OptionalString) StringValue() string {
	return o.value
}

// OrElse returns the OptionalString's value if it has one, or else the specified fallback value.
func (o OptionalString) OrElse(valueIfEmpty string) string {
	if o.hasValue {
		return o.value
	}
	return valueIfEmpty
}

// AsPointer returns the OptionalString's value as a string pointer if it has a value, or
// nil otherwise.
//
// The string value, if any, is copied rather than returning to a pointer to the internal field.
func (o OptionalString) AsPointer() *string {
	if o.hasValue {
		s := o.value
		return &s
	}
	return nil
}

// AsValue converts the OptionalString to a Value, which is either Null() or a string value.
func (o OptionalString) AsValue() Value {
	if o.hasValue {
		return String(o.value)
	}
	return Null()
}

// String is a debugging convenience method that returns a description of the OptionalString.
// This is either the same as its string value, "[empty]" if it has a string value that is empty,
// or "[none]" if it has no value.
//
// Since String() is used by methods like fmt.Printf, if you want to use the actual string value
// of the OptionalString instead of this special representation, you should call StringValue():
//
//     s := OptionalString{}
//     fmt.Printf("it is '%s'", s) // prints "it is '[none]'"
//     fmt.Printf("it is '%s'", s.StringValue()) // prints "it is ''"
func (o OptionalString) String() string {
	if o.hasValue {
		if o.value == "" {
			return "[empty]"
		}
		return o.value
	}
	return "[none]"
}

// MarshalJSON converts the OptionalString to its JSON representation.
//
// The output will be either a JSON string or null. Note that the "omitempty" tag for a struct
// field will not cause an empty OptionalString field to be omitted; it will be output as null.
// If you want to completely omit a JSON property when there is no value, it must be a string
// pointer instead of an OptionalString; use the AsPointer() method to get a pointer.
func (o OptionalString) MarshalJSON() ([]byte, error) {
	if o.hasValue {
		return json.Marshal(o.value)
	}
	return []byte("null"), nil
}

// UnmarshalJSON parses an OptionalString from JSON.
//
// The input must be either a JSON string or null.
func (o *OptionalString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 { // should not be possible, the parser doesn't pass empty slices to UnmarshalJSON
		return errors.New("cannot parse empty data")
	}
	firstCh := data[0]
	switch firstCh {
	case 'n':
		// Note that since Go 1.5, comparing a string to string([]byte) is optimized so it
		// does not actually create a new string from the byte slice.
		if string(data) == "null" {
			*o = OptionalString{}
			return nil
		}
	case '"':
		var s string
		e := json.Unmarshal(data, &s)
		if e == nil {
			*o = NewOptionalString(s)
		}
		return e
	}
	*o = OptionalString{}
	return fmt.Errorf("unknown JSON token: %s", data)
}
