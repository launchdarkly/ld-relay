package ldvalue

// string literals should be defined here if they are referenced in multiple files

const nullAsJSON = "null"

// ValueType is defined in value_base.go

const (
	// NullType describes a null value. Note that the zero value of ValueType is NullType, so the
	// zero of Value is a null value.
	NullType ValueType = iota
	// BoolType describes a boolean value.
	BoolType ValueType = iota
	// NumberType describes a numeric value. JSON does not have separate types for int and float, but
	// you can convert to either.
	NumberType ValueType = iota
	// StringType describes a string value.
	StringType ValueType = iota
	// ArrayType describes an array value.
	ArrayType ValueType = iota
	// ObjectType describes an object (a.k.a. map).
	ObjectType ValueType = iota
	// RawType describes a json.RawMessage value. This value will not be parsed or interpreted as
	// any other data type, and can be accessed only by calling AsRaw().
	RawType ValueType = iota
)
