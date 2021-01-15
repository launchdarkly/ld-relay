package util

import "sync"

// StringMemoizer is a simple encapsulation of a lazily evaluated-only-once string function.
type StringMemoizer struct {
	encodeOnce sync.Once
	computeFn  func() string
	result     string
}

// NewStringMemoizer creates a new uninitialized StringMemoizer.
func NewStringMemoizer(computeFn func() string) *StringMemoizer {
	return &StringMemoizer{computeFn: computeFn}
}

// Get returns the result of the computeFn, calling it only if it has not already been called.
func (m *StringMemoizer) Get() string {
	m.encodeOnce.Do(func() {
		m.result = m.computeFn()
	})
	return m.result
}
