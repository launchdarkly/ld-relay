package sharedtest

import "github.com/launchdarkly/go-server-sdk/v6/subsystems"

type ExistingDataSourceFactory struct{ Instance subsystems.DataSource }
type DataSourceThatNeverStarts struct{}
type DataSourceThatStartsWithoutInitializing struct{}

func (f ExistingDataSourceFactory) CreateDataSource(
	context subsystems.ClientContext,
	updates subsystems.DataSourceUpdates,
) (subsystems.DataSource, error) {
	return f.Instance, nil
}

func (d DataSourceThatNeverStarts) Start(chan<- struct{}) {}
func (d DataSourceThatNeverStarts) IsInitialized() bool   { return false }
func (d DataSourceThatNeverStarts) Close() error          { return nil }

func (d DataSourceThatStartsWithoutInitializing) Start(closeWhenReady chan<- struct{}) {
	go close(closeWhenReady)
}
func (d DataSourceThatStartsWithoutInitializing) IsInitialized() bool { return false }
func (d DataSourceThatStartsWithoutInitializing) Close() error        { return nil }
