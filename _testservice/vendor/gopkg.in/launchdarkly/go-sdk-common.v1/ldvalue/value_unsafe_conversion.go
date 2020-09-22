package ldvalue

import "encoding/json"

// This file contains methods to convert between Value and the empty interface{} type without
// deep-copying. These will be removed in a future release for Go SDK v5.

// UnsafeUseArbitraryValue creates a Value that wraps an arbitrary Go value as-is. Application
// code should never use this method, since it can break the immutability contract of Value.
//
// This method and Value.UnsafeArbitraryValue() are provided for backward compatibility in v4 of
// the Go SDK, where flag values are stored internally as interface{} and can be accessed as
// interface{} with JsonVariation() or JsonVariationDetail(); using the original value avoids
// adding an unexpected deep-copy step for code that is using those methods. In a future version,
// that behavior will be removed and the SDK will use only immutable Value instances.
//
// The allowable value types are the same as for CopyArbitraryValue.
//
// Deprecated: Application code should use CopyArbitraryValue instead of this (or just use the
// regular value constructors such as Bool() and ObjectBuild()). This method will be removed in a
// future version.
func UnsafeUseArbitraryValue(valueAsInterface interface{}) Value {
	if valueAsInterface == nil {
		return Null()
	}
	switch o := valueAsInterface.(type) {
	case []interface{}:
		return Value{valueType: ArrayType, unsafeValueInstance: o}
	case map[string]interface{}:
		return Value{valueType: ObjectType, unsafeValueInstance: o}
	case json.RawMessage:
		return Value{valueType: RawType, unsafeValueInstance: o}
	default:
		// For primitive types, we don't bother wrapping the original interface{}, we just convert it
		// to our own representation of the primitive type. That's because the overhead from copying
		// the value back to an interface{}, if the application requested it that way, is negligible
		// and applications are unlikely to be using JsonVariation for primitive types anyway. It
		// matters more with arrays and objects, where (until we remove the deprecated JsonVariation)
		// we don't want it to suddenly be incurring the overhead of a deep copy when they get the
		// value.
		return CopyArbitraryValue(valueAsInterface)
	}
}

// UnsafeArbitraryValue converts the Value to its corresponding Go type as an interface{} -
// or, if it was created from an existing slice or map using UnsafeUseArbitraryValue, it
// returns that original value as an interface{} without deep-copying. See
// UnsafeUseArbitraryValue for more information.
//
// Deprecated: Application code should use AsArbitraryValue instead. This method will be
// removed in a future version.
func (v Value) UnsafeArbitraryValue() interface{} {
	if v.unsafeValueInstance != nil {
		return v.unsafeValueInstance
	}
	return v.AsArbitraryValue()
}
