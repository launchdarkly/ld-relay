package sharedtest

import (
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// FlagDescriptor is a shortcut for creating a StoreItemDescriptor from a flag.
func FlagDescriptor(f ldmodel.FeatureFlag) interfaces.StoreItemDescriptor {
	return interfaces.StoreItemDescriptor{Version: f.Version, Item: &f}
}

// SegmentDescriptor is a shortcut for creating a StoreItemDescriptor from a segment.
func SegmentDescriptor(s ldmodel.Segment) interfaces.StoreItemDescriptor {
	return interfaces.StoreItemDescriptor{Version: s.Version, Item: &s}
}
