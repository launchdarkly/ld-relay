package sharedtest

import "github.com/launchdarkly/go-server-sdk/v6/subsystems"

type dataSourceThatNeverStarts struct{}
type dataSourceThatStartsWithoutInitializing struct{}

func DataSourceThatNeverStarts() subsystems.DataSource {
	return dataSourceThatNeverStarts{}
}

func DataSourceThatStartsWithoutInitializing() subsystems.DataSource {
	return dataSourceThatStartsWithoutInitializing{}
}

func (d dataSourceThatNeverStarts) Start(chan<- struct{}) {}
func (d dataSourceThatNeverStarts) IsInitialized() bool   { return false }
func (d dataSourceThatNeverStarts) Close() error          { return nil }

func (d dataSourceThatStartsWithoutInitializing) Start(closeWhenReady chan<- struct{}) {
	go close(closeWhenReady)
}
func (d dataSourceThatStartsWithoutInitializing) IsInitialized() bool { return false }
func (d dataSourceThatStartsWithoutInitializing) Close() error        { return nil }
