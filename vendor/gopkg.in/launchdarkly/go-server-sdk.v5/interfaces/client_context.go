package interfaces

// ClientContext provides context information from LDClient when creating other components.
//
// This is passed as a parameter to the factory methods for implementations of DataStore, DataSource,
// etc. The actual implementation type may contain other properties that are only relevant to the built-in
// SDK components and are therefore not part of the public interface; this allows the SDK to add its own
// context information as needed without disturbing the public API.
type ClientContext interface {
	// GetBasicConfiguration returns the SDK's basic global properties.
	GetBasic() BasicConfiguration
	// GetHTTP returns the configured HTTPConfiguration.
	GetHTTP() HTTPConfiguration
	// GetLogging returns the configured LoggingConfiguration.
	GetLogging() LoggingConfiguration
}

// BasicConfiguration contains the most basic properties of the SDK client that are available
// to all SDK component factories.
type BasicConfiguration struct {
	// SDKKey is the configured SDK key.
	SDKKey string

	// Offline is true if the client was configured to be completely offline.
	Offline bool
}
