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
	b.copyOnWrite = true
	return Value{valueType: ObjectType, immutableObjectValue: b.output}
}

// Count returns the number of elements in an array or JSON object.
//
// For values of any other type, it returns zero.
func (v Value) Count() int {
	switch v.valueType {
	case ArrayType:
		if v.immutableArrayValue != nil {
			return len(v.immutableArrayValue)
		}
		if a, ok := v.unsafeValueInstance.([]interface{}); ok {
			return len(a)
		}
	case ObjectType:
		if v.immutableObjectValue != nil {
			return len(v.immutableObjectValue)
		}
		if m, ok := v.unsafeValueInstance.(map[string]interface{}); ok {
			return len(m)
		}
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
		if v.immutableArrayValue != nil {
			if index >= 0 && index < len(v.immutableArrayValue) {
				return v.immutableArrayValue[index], true
			}
		} else if a, ok := v.unsafeValueInstance.([]interface{}); ok {
			if index >= 0 && index < len(a) {
				return CopyArbitraryValue(a[index]), true
			}
		}
	}
	return Null(), false
}

// Keys returns the keys of a JSON object as a slice.
//
// The method copies the keys. If the value is not an object, it returns an empty slice.
func (v Value) Keys() []string {
	if v.valueType == ObjectType {
		if v.immutableObjectValue != nil {
			ret := make([]string, len(v.immutableObjectValue))
			i := 0
			for key := range v.immutableObjectValue {
				ret[i] = key
				i++
			}
			return ret
		}
		if m, ok := v.unsafeValueInstance.(map[string]interface{}); ok {
			ret := make([]string, len(m))
			i := 0
			for key := range m {
				ret[i] = key
				i++
			}
			return ret
		}
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
		if v.immutableObjectValue != nil {
			ret, ok := v.immutableObjectValue[name]
			return ret, ok
		}
		if m, ok := v.unsafeValueInstance.(map[string]interface{}); ok {
			if innerValue, ok := m[name]; ok {
				return CopyArbitraryValue(innerValue), true
			}
		}
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
