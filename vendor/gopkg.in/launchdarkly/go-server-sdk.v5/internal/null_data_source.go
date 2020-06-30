package internal

import "gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"

// NewNullDataSource returns a stub implementation of DataSource.
func NewNullDataSource() interfaces.DataSource {
	return nullDataSource{}
}

type nullDataSource struct{}

func (n nullDataSource) IsInitialized() bool {
	return true
}

func (n nullDataSource) Close() error {
	return nil
}

func (n nullDataSource) Start(closeWhenReady chan<- struct{}) {
	close(closeWhenReady)
}
