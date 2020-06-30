package ldvalue

// This file contains types and methods that are only used for complex data structures (array and
// object), in the fully immutable model where no slices, maps, or interface{} values are exposed.

// ArrayBuilder is a builder created by ArrayBuild(), for creating immutable arrays.
type ArrayBuilder interface {
	// Add appends an element to the array builder.
	Add(value Value) ArrayBuilder
	// Build creates a Value containing the previously added array elements. Continuing to modify the
	// same builder by calling Add after that point does not affect the returned array.
	Build() Value
}

type arrayBuilderImpl struct {
	copyOnWrite bool
	output      []Value
}

// ObjectBuilder is a builder created by ObjectBuild(), for creating immutable JSON objects.
type ObjectBuilder interface {
	// Set sets a key-value pair in the object builder.
	Set(key string, value Value) ObjectBuilder
	// Build creates a Value containing the previously specified key-value pairs. Continuing to modify
	// the same builder by calling Set after that point does not affect the returned object.
	Build() Value
}

type objectBuilderImpl struct {
	copyOnWrite bool
	output      map[string]Value
}

// ArrayOf creates an array Value from a list of Values.
//
// This requires a slice copy to ensure immutability; otherwise, an existing slice could be passed
// using the spread operator, and then modified. However, since Value is itself immutable, it does
// not need to deep-copy each item.
func ArrayOf(items ...Value) Value {
	if len(items) == 0 {
		return Value{valueType: ArrayType} // no need to allocate an empty array
	}
	copiedItems := make([]Value, len(items))
	copy(copiedItems, items)
	return Value{valueType: ArrayType, immutableArrayValue: copiedItems}
}

// ArrayBuild creates a builder for constructing an immutable array Value.
//
//     arrayValue := ldvalue.ArrayBuild().Add(ldvalue.Int(100)).Add(ldvalue.Int(200)).Build()
func ArrayBuild() ArrayBuilder {
	return ArrayBuildWithCapacity(1)
}

// ArrayBuildWithCapacity creates a builder for constructing an immutable array Value.
//
// The capacity parameter is the same as the capacity of a slice, allowing you to preallocate space
// if you know the number of elements; otherwise you can pass zero.
//
//     arrayValue := ldvalue.ArrayBuildWithCapacity(2).Add(ldvalue.Int(100)).Add(ldvalue.Int(200)).Build()
func ArrayBuildWithCapacity(capacity int) ArrayBuilder {
	return &arrayBuilderImpl{output: make([]Value, 0, capacity)}
}

func (b *arrayBuilderImpl) Add(value Value) ArrayBuilder {
	if b.copyOnWrite {
		n := len(b.output)
		newSlice := make([]Value, n, n+1)
		copy(newSlice[0:n], b.output)
		b.output = newSlice
		b.copyOnWrite = false
	}
	b.output = append(b.output, value)
	return b
}

func (b *arrayBuilderImpl) Build() Value {
	if len(b.output) == 0 {
		return Value{valueType: ArrayType} // don't bother retaining an empty slice
	}
	b.copyOnWrite = true
	return Value{valueType: ArrayType, immutableArrayValue: b.output}
}

// CopyObject creates a Value by copying an existing map[string]Value.
//
// If you want to copy a map[string]interface{} instead, use CopyArbitraryValue.
func CopyObject(m map[string]Value) Value {
	return Value{valueType: ObjectType, immutableObjectValue: copyValueMap(m)}
}

// ObjectBuild creates a builder for constructing an immutable JSON object Value.
//
//     objValue := ldvalue.ObjectBuild().Set("a", ldvalue.Int(100)).Set("b", ldvalue.Int(200)).Build()
func ObjectBuild() ObjectBuilder {
	return ObjectBuildWithCapacity(1)
}

// ObjectBuildWithCapacity creates a builder for constructing an immutable JSON object Value.
//
// The capacity parameter is the same as the capacity of a map, allowing you to preallocate space
// if you know the number of elements; otherwise you can pass zero.
//
//     objValue := ldvalue.ObjectBuildWithCapacity(2).Set("a", ldvalue.Int(100)).Set("b", ldvalue.Int(200)).Build()
func ObjectBuildWithCapacity(capacity int) ObjectBuilder {
	return &objectBuilderImpl{output: make(map[string]Value, capacity)}
}

func (b *objectBuilderImpl) Set(name string, value Value) ObjectBuilder {
	if b.copyOnWrite {
		b.output = copyValueMap(b.output)
		b.copyOnWrite = false
	}
	b.output[name] = value
	return b
}

func (b *objectBuilderImpl) Build() Value {
	if len(b.output) == 0 {
		return Value{valueType: ObjectType} // don't bother retaining an empty map
	}
	b.copyOnWrite = true
	return Value{valueType: ObjectType, immutableObjectValue: b.output}
}

// Count returns the number of elements in an array or JSON object.
//
// For values of any other type, it returns zero.
func (v Value) Count() int {
	switch v.valueType {
	case ArrayType:
		return len(v.immutableArrayValue)
	case ObjectType:
		return len(v.immutableObjectValue)
	}
	return 0
}

// GetByIndex gets an element of an array by index.
//
// If the value is not an array, or if the index is out of range, it returns Null().
func (v Value) GetByIndex(index int) Value {
	ret, _ := v.TryGetByIndex(index)
	return ret
}

// TryGetByIndex gets an element of an array by index, with a second return value of true if
// successful.
//
// If the value is not an array, or if the index is out of range, it returns (Null(), false).
func (v Value) TryGetByIndex(index int) (Value, bool) {
	if v.valueType == ArrayType {
		if index >= 0 && index < len(v.immutableArrayValue) {
			return v.immutableArrayValue[index], true
		}
	}
	return Null(), false
}

// Keys returns the keys of a JSON object as a slice.
//
// The method copies the keys. If the value is not an object, it returns an empty slice.
func (v Value) Keys() []string {
	if v.valueType == ObjectType {
		ret := make([]string, len(v.immutableObjectValue))
		i := 0
		for key := range v.immutableObjectValue {
			ret[i] = key
			i++
		}
		return ret
	}
	return nil
}

// GetByKey gets a value from a JSON object by key.
//
// If the value is not an object, or if the key is not found, it returns Null().
func (v Value) GetByKey(name string) Value {
	ret, _ := v.TryGetByKey(name)
	return ret
}

// TryGetByKey gets a value from a JSON object by key, with a second return value of true if
// successful.
//
// If the value is not an object, or if the key is not found, it returns (Null(), false).
func (v Value) TryGetByKey(name string) (Value, bool) {
	if v.valueType == ObjectType {
		ret, ok := v.immutableObjectValue[name]
		return ret, ok
	}
	return Null(), false
}

func copyValueMap(m map[string]Value) map[string]Value {
	ret := make(map[string]Value, len(m))
	for k, v := range m {
		ret[k] = v
	}
	return ret
}

// Enumerate calls a function for each value within a Value.
//
// If the input value is Null(), the function is not called.
//
// If the input value is an array, fn is called for each element, with the element's index in the
// first parameter, "" in the second, and the element value in the third.
//
// If the input value is an object, fn is called for each key-value pair, with 0 in the first
// parameter, the key in the second, and the value in the third.
//
// For any other value type, fn is called once for that value.
//
// The return value of fn is true to continue enumerating, false to stop.
func (v Value) Enumerate(fn func(index int, key string, value Value) bool) {
	switch v.valueType {
	case NullType:
		return
	case ArrayType:
		for i, v1 := range v.immutableArrayValue {
			if !fn(i, "", v1) {
				return
			}
		}
	case ObjectType:
		for k, v1 := range v.immutableObjectValue {
			if !fn(0, k, v1) {
				return
			}
		}
	default:
		_ = fn(0, "", v)
	}
}

// Transform applies a transformation function to a Value, returning a new Value.
//
// The behavior is as follows:
//
// If the input value is Null(), the return value is always Null() and the function is not called.
//
// If the input value is an array, fn is called for each element, with the element's index in the
// first parameter, "" in the second, and the element value in the third. The return values of fn
// can be either a transformed value and true, or any value and false to remove the element.
//
//     ldvalue.ArrayOf(ldvalue.Int(1), ldvalue.Int(2), ldvalue.Int(3)).Build().
//         Transform(func(index int, key string, value Value) (Value, bool) {
//             if value.IntValue() == 2 {
//                 return ldvalue.Null(), false
//             }
//             return ldvalue.Int(value.IntValue() * 10), true
//         })
//     // returns [10, 30]
//
// If the input value is an object, fn is called for each key-value pair, with 0 in the first
// parameter, the key in the second, and the value in the third. Again, fn can choose to either
// transform or drop the value.
//
//     ldvalue.ObjectBuild().Set("a", ldvalue.Int(1)).Set("b", ldvalue.Int(2)).Set("c", ldvalue.Int(3)).Build().
//         Transform(func(index int, key string, value Value) (Value, bool) {
//             if key == "b" {
//                 return ldvalue.Null(), false
//             }
//             return ldvalue.Int(value.IntValue() * 10), true
//         })
//     // returns {"a": 10, "c": 30}
//
// For any other value type, fn is called once for that value; if it provides a transformed
// value and true, the transformed value is returned, otherwise Null().
//
//     ldvalue.Int(2).Transform(func(index int, key string, value Value) (Value, bool) {
//         return ldvalue.Int(value.IntValue() * 10), true
//     })
//     // returns numeric value of 20
//
// For array and object values, if the function does not modify or drop any values, the exact
// same instance is returned without allocating a new slice or map.
func (v Value) Transform(fn func(index int, key string, value Value) (Value, bool)) Value {
	switch v.valueType {
	case NullType:
		return v
	case ArrayType:
		return Value{valueType: ArrayType, immutableArrayValue: transformArray(v.immutableArrayValue, fn)}
	case ObjectType:
		return Value{valueType: ObjectType, immutableObjectValue: transformObject(v.immutableObjectValue, fn)}
	default:
		if transformedValue, ok := fn(0, "", v); ok {
			return transformedValue
		}
		return Null()
	}
}

func transformArray(values []Value, fn func(index int, key string, value Value) (Value, bool)) []Value {
	ret := values
	startedNewSlice := false
	for i, v := range values {
		transformedValue, ok := fn(i, "", v)
		modified := !ok || !transformedValue.Equal(v)
		if modified && !startedNewSlice {
			// This is the first change we've seen, so we should start building a new slice and
			// retroactively add any values to it that already passed the test without changes.
			startedNewSlice = true
			if i == 0 {
				ret = nil // Avoid allocating an array until we know it'll be non-empty
			} else {
				ret = make([]Value, i, len(values))
				copy(ret, values)
			}
		}
		if startedNewSlice && ok {
			if ret == nil {
				ret = make([]Value, 0, len(values))
			}
			ret = append(ret, transformedValue)
		}
	}
	return ret
}

func transformObject(
	values map[string]Value,
	fn func(index int, key string, value Value,
	) (Value, bool)) map[string]Value {
	ret := values
	startedNewMap := false
	seenKeys := make([]string, 0, len(values))
	for k, v := range values {
		transformedValue, ok := fn(0, k, v)
		modified := !ok || !transformedValue.Equal(v)
		if modified && !startedNewMap {
			// This is the first change we've seen, so we should start building a new map and
			// retroactively add any values to it that already passed the test without changes.
			startedNewMap = true
			if len(seenKeys) == 0 {
				ret = nil // Avoid allocating a map until we know it'll be non-empty
			} else {
				ret = make(map[string]Value, len(values))
				for _, seenKey := range seenKeys {
					ret[seenKey] = values[seenKey]
				}
			}
		} else {
			seenKeys = append(seenKeys, k)
		}
		if startedNewMap && ok {
			if ret == nil {
				ret = make(map[string]Value, len(values))
			}
			ret[k] = transformedValue
		}
	}
	return ret
}
