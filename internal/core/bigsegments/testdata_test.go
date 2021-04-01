package bigsegments

var (
	patch1 = bigSegmentPatch{
		EnvironmentID:   "abc",
		SegmentID:       "segment.g1",
		Version:         "1",
		PreviousVersion: "",
		Changes: bigSegmentPatchChanges{
			Included: bigSegmentPatchChangesMutations{
				Add: []string{"included1", "included2"},
			},
			Excluded: bigSegmentPatchChangesMutations{
				Add: []string{"excluded1", "excluded2"},
			},
		},
	}

	patch2 = bigSegmentPatch{
		EnvironmentID:   "abc",
		SegmentID:       "segment.g1",
		Version:         "2",
		PreviousVersion: "1",
		Changes: bigSegmentPatchChanges{
			Included: bigSegmentPatchChangesMutations{
				Remove: []string{"included1"},
			},
			Excluded: bigSegmentPatchChangesMutations{
				Remove: []string{"excluded1"},
			},
		},
	}
)
