package interfaces

import "io"

// DataSourceFactory is a factory that creates some implementation of DataSource.
type DataSourceFactory interface {
	// CreateDataSource is called by the SDK to create the implementation instance.
	//
	// This happens only when MakeClient or MakeCustomClient is called. The implementation instance
	// is then tied to the life cycle of the LDClient, so it will receive a Close() call when the
	// client is closed.
	//
	// If the factory returns an error, creation of the LDClient fails.
	CreateDataSource(
		context ClientContext,
		dataSourceUpdates DataSourceUpdates,
	) (DataSource, error)
}

// DataSource describes the interface for an object that receives feature flag data.
type DataSource interface {
	io.Closer

	// IsInitialized returns true if the data source has successfully initialized at some point.
	//
	// Once this is true, it should remain true even if a problem occurs later.
	IsInitialized() bool

	// Start tells the data source to begin initializing. It should not try to make any connections
	// or do any other significant activity until Start is called.
	//
	// The data source should close the closeWhenReady channel if and when it has either successfully
	// initialized for the first time, or determined that initialization cannot ever succeed.
	Start(closeWhenReady chan<- struct{})
}
