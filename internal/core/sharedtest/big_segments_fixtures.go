package sharedtest

import "gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"

// NoOpSDKBigSegmentStore is a stub implementation of the SDK's BigSegmentStore (not the type
// of the same name that Relay uses internally).
type NoOpSDKBigSegmentStore struct{}

func (m *NoOpSDKBigSegmentStore) Close() error {
	return nil
}

func (m *NoOpSDKBigSegmentStore) GetMetadata() (interfaces.BigSegmentStoreMetadata, error) {
	return interfaces.BigSegmentStoreMetadata{}, nil
}

func (m *NoOpSDKBigSegmentStore) GetUserMembership(
	userHash string,
) (interfaces.BigSegmentMembership, error) {
	return nil, nil
}
