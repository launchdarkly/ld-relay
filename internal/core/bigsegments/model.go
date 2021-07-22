package bigsegments

// bigSegmentPatchChangesMutations lists users that should be added or removed
// to either the included or excluded set of a big segment.
type bigSegmentPatchChangesMutations struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

// bigSegmentPatchChanges represents changes to the included and excluded sets
// of a segment.
type bigSegmentPatchChanges struct {
	Included bigSegmentPatchChangesMutations `json:"included"`
	Excluded bigSegmentPatchChangesMutations `json:"excluded"`
}

// bigSegmentPatch represents a patch of of a big segment in an environment.
type bigSegmentPatch struct {
	EnvironmentID   string                 `json:"environmentId"`
	SegmentID       string                 `json:"segmentId"`
	Version         string                 `json:"version"`
	PreviousVersion string                 `json:"previousVersion"`
	Changes         bigSegmentPatchChanges `json:"changes"`
}
