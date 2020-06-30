package sharedtest

import "reflect"

// AssertNotNil forces a panic if the specified value is nil (either a nil interface value, or a
// nil pointer).
func AssertNotNil(i interface{}) {
	if i != nil {
		val := reflect.ValueOf(i)
		if val.Kind() != reflect.Ptr || !val.IsNil() {
			return
		}
	}
	panic("unexpected nil pointer or nil interface value")
}
