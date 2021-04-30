package sharedtest

import "gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"

type ExistingDataSourceFactory struct{ Instance interfaces.DataSource }
type DataSourceThatNeverStarts struct{}
type DataSourceThatStartsWithoutInitializing struct{}

func (f ExistingDataSourceFactory) CreateDataSource(
	context interfaces.ClientContext,
	updates interfaces.DataSourceUpdates,
) (interfaces.DataSource, error) {
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
