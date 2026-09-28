package util

// PtrOrNil returns a pointer to v, or nil when v is the zero value for its type. It is used to model
// optional fields where the zero value (e.g. an empty string or the zero time.Time) means "absent".
func PtrOrNil[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}
