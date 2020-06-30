package ldmodel

// TargetContainsKey returns true if the specified user key is in this Target.
//
// This part of the flag evaluation logic is defined in ldmodel and exported, rather than being
// internal to Evaluator, as a compromise to allow for optimizations that require storing precomputed
// data in the model object. Exporting this function is preferable to exporting those internal
// implementation details.
//
// The target passed by reference for efficiency only; the function will not modify it. Passing a
// nil value will cause a panic.
func TargetContainsKey(t *Target, key string) bool {
	if t.preprocessed.valuesMap != nil {
		return t.preprocessed.valuesMap[key]
	}
	for _, value := range t.Values {
		if value == key {
			return true
		}
	}
	return false
}
