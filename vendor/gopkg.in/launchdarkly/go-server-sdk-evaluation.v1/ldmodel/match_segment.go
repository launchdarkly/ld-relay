package ldmodel

// SegmentIncludesOrExcludesKey tests whether the specified user key is in the include or exclude
// list of this Segment. If it is in either, then the first return value is true for include or false
// for exclude, and the second return value is true. If it is in neither, both return values are fale.
//
// This part of the flag evaluation logic is defined in ldmodel and exported, rather than being
// internal to Evaluator, as a compromise to allow for optimizations that require storing precomputed
// data in the model object. Exporting this function is preferable to exporting those internal
// implementation details.
//
// The segment passed by reference for efficiency only; the function will not modify it. Passing a
// nil value will cause a panic.
func SegmentIncludesOrExcludesKey(s *Segment, userKey string) (included bool, found bool) {
	// Check if the user is included in the segment by key
	if s.preprocessed.includeMap == nil {
		for _, key := range s.Included {
			if userKey == key {
				return true, true
			}
		}
	} else if s.preprocessed.includeMap[userKey] {
		return true, true
	}

	// Check if the user is excluded from the segment by key
	if s.preprocessed.excludeMap == nil {
		for _, key := range s.Excluded {
			if userKey == key {
				return false, true
			}
		}
	} else if s.preprocessed.excludeMap[userKey] {
		return false, true
	}

	return false, false
}
