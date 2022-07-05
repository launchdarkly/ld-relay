package sharedtest

import "github.com/launchdarkly/go-server-sdk/v6/subsystems"

// NoOpSDKBigSegmentStore is a stub implementation of the SDK's BigSegmentStore (not the type
// of the same name that Relay uses internally).
type NoOpSDKBigSegmentStore struct{}

func (m *NoOpSDKBigSegmentStore) Close() error {
	return nil
}

func (m *NoOpSDKBigSegmentStore) GetMetadata() (subsystems.BigSegmentStoreMetadata, error) {
	return subsystems.BigSegmentStoreMetadata{}, nil
}

func (m *NoOpSDKBigSegmentStore) GetMembership(
	contextHash string,
) (subsystems.BigSegmentMembership, error) {
	return nil, nil
}
