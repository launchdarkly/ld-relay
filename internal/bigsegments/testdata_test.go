package bigsegments

import (
	"encoding/json"

	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
)

const testSDKKey = "sdk-key"
const testEnvironmentID = "abc"

func makePatchEvent(patches ...bigSegmentPatch) *httphelpers.SSEEvent {
	bytes, err := json.Marshal(patches)
	if err != nil {
		panic(err)
	}
	return &httphelpers.SSEEvent{Data: string(bytes)}
}

type patchBuilder struct {
	patch bigSegmentPatch
}

func newPatchBuilder(segmentID, version, previous string) *patchBuilder {
	return &patchBuilder{
		patch: bigSegmentPatch{
			EnvironmentID:   testEnvironmentID,
			SegmentID:       segmentID,
			Version:         version,
			PreviousVersion: previous,
		},
	}
}

func (b *patchBuilder) addIncludes(keys ...string) *patchBuilder {
	b.patch.Changes.Included.Add = append(b.patch.Changes.Included.Add, keys...)
	return b
}

func (b *patchBuilder) addExcludes(keys ...string) *patchBuilder {
	b.patch.Changes.Excluded.Add = append(b.patch.Changes.Excluded.Add, keys...)
	return b
}

func (b *patchBuilder) removeIncludes(keys ...string) *patchBuilder {
	b.patch.Changes.Included.Remove = append(b.patch.Changes.Included.Remove, keys...)
	return b
}

func (b *patchBuilder) removeExcludes(keys ...string) *patchBuilder {
	b.patch.Changes.Excluded.Remove = append(b.patch.Changes.Excluded.Remove, keys...)
	return b
}

func (b *patchBuilder) build() bigSegmentPatch {
	return b.patch
}
