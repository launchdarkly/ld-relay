package relay

import (
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/filedata"

	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

const (
	logMsgOfflineEnvTimeoutError          = "Unable to initialize offline environment %q: timed out waiting for client creation"
	logMsgInternalErrorUpdatedEnvNotFound = "Unexpected error in file data processing: environment ID %s not found when updating"
	logMsgInternalErrorNoUpdatesForEnv    = "Unexpected error in file data processing: environment ID %s not found in envUpdates"
)

// relayFileDataActions is an implementation of the filedata.UpdateHandler interface. The low-level
// filedata.ArchiveManager component, which manages the file data source, will call the interface
// methods on this object to let us know when environments have been read from the file for the
// first time and also if environments have changed due to a file update.
type relayFileDataActions struct {
	r                *Relay
	envSynchronizers map[config.EnvironmentID]*filedata.OfflineModeSynchronizer
}

func (a *relayFileDataActions) AddEnvironment(ae filedata.ArchiveEnvironment) {
	// Create a channel to pass the archive data to the offline synchronizer
	dataCh := make(chan []ldstoretypes.Collection, 1)

	// Create the synchronizer instance that we can later update when the file changes
	synchronizer := filedata.NewOfflineModeSynchronizer(dataCh)

	transformConfig := func(baseConfig ld.Config) ld.Config {
		config := baseConfig
		// In offline mode, replace the DataSystem with our custom offline synchronizer.
		// This synchronizer loads data from the archive file without making network connections.
		syncFactory := filedata.OfflineModeSynchronizerFactory{Synchronizer: synchronizer}
		config.DataSystem = ldcomponents.DataSystem().
			Custom().
			Synchronizers(syncFactory, syncFactory) // primary and fallback use the same synchronizer
		config.Events = ldcomponents.NoEvents()
		return config
	}

	envConfig := envfactory.NewEnvConfigFactoryForOfflineMode(a.r.config.OfflineMode).MakeEnvironmentConfig(ae.Params)
	env, _, err := a.r.addEnvironment(ae.Params.Identifiers, envConfig, transformConfig)
	if err != nil {
		a.r.loggers.Errorf(logMsgAutoConfEnvInitError, ae.Params.Identifiers.GetDisplayName(), err)
		return
	}

	if ae.Params.ExpiringSDKKey.Defined() {
		update := relayenv.NewCredentialUpdate(ae.Params.SDKKey)
		env.UpdateCredential(update.WithGracePeriod(ae.Params.ExpiringSDKKey.Key, ae.Params.ExpiringSDKKey.Expiration))
	}

	// Store the synchronizer so we can update it later when the file changes
	if a.envSynchronizers == nil {
		a.envSynchronizers = make(map[config.EnvironmentID]*filedata.OfflineModeSynchronizer)
	}
	a.envSynchronizers[ae.Params.EnvID] = synchronizer

	// Send the initial archive data to the synchronizer
	// The synchronizer will be waiting for this data in its Sync() method
	dataCh <- ae.SDKData
}

func (a *relayFileDataActions) UpdateEnvironment(ae filedata.ArchiveEnvironment) {
	env, _ := a.r.getEnvironment(sdkauth.NewScoped(ae.Params.Identifiers.FilterKey, ae.Params.EnvID))
	if env == nil { // COVERAGE: this should never happen and can't be covered in unit tests
		a.r.loggers.Errorf(logMsgInternalErrorUpdatedEnvNotFound, ae.Params.EnvID)
		return
	}
	synchronizer := a.envSynchronizers[ae.Params.EnvID]
	if synchronizer == nil { // COVERAGE: this should never happen and can't be covered in unit tests
		a.r.loggers.Errorf(logMsgInternalErrorNoUpdatesForEnv, ae.Params.EnvID)
		return
	}

	env.SetIdentifiers(ae.Params.Identifiers)
	env.SetTTL(ae.Params.TTL)
	env.SetSecureMode(ae.Params.SecureMode)

	if ae.Params.MobileKey.Defined() {
		env.UpdateCredential(relayenv.NewCredentialUpdate(ae.Params.MobileKey))
	}
	if ae.Params.SDKKey.Defined() {
		update := relayenv.NewCredentialUpdate(ae.Params.SDKKey)
		if ae.Params.ExpiringSDKKey.Defined() {
			update = update.WithGracePeriod(ae.Params.ExpiringSDKKey.Key, ae.Params.ExpiringSDKKey.Expiration)
		}
		env.UpdateCredential(update)
	}

	// SDKData will be non-nil only if the flag/segment data for the environment has actually changed.
	if ae.SDKData != nil {
		if err := synchronizer.UpdateData(ae.SDKData); err != nil {
			a.r.loggers.Errorf("Error updating offline environment data: %v", err)
		}
	}
}

func (a *relayFileDataActions) EnvironmentFailed(id config.EnvironmentID, err error) {
	// error logging goes here
}

func (a *relayFileDataActions) DeleteEnvironment(id config.EnvironmentID, filter config.FilterKey) {
	a.r.removeEnvironment(sdkauth.NewScoped(filter, id))
	delete(a.envSynchronizers, id)
}
