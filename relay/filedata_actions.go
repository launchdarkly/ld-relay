package relay

import (
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"

	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/filedata"

	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
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
	// Create the synchronizer with the initial archive data
	synchronizer := filedata.NewOfflineModeSynchronizer(ae.SDKData)

	transformConfig := func(baseConfig ld.Config) ld.Config {
		config := baseConfig
		// In offline mode, replace the DataSystem with our custom offline synchronizer.
		// This synchronizer loads data from the archive file without making network connections.
		syncFactory := filedata.OfflineModeSynchronizerFactory{Synchronizer: synchronizer}
		config.DataSystem = ldcomponents.DataSystem().
			Custom().
			Synchronizers(syncFactory)
		config.Events = ldcomponents.NoEvents()
		return config
	}

	envConfig := envfactory.NewEnvConfigFactoryForOfflineMode(a.r.config.OfflineMode).MakeEnvironmentConfig(ae.Params)
	env, _, err := a.r.addEnvironment(ae.Params.Identifiers, envConfig, transformConfig)
	if err != nil {
		a.r.logger.Error("unable to initialize offline environment", "env", ae.Params.Identifiers.GetDisplayName(), "error", err)
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
}

func (a *relayFileDataActions) UpdateEnvironment(ae filedata.ArchiveEnvironment) {
	env, _ := a.r.getEnvironment(sdkauth.NewScoped(ae.Params.Identifiers.FilterKey, ae.Params.EnvID))
	if env == nil { // COVERAGE: this should never happen and can't be covered in unit tests
		a.r.logger.Error("unexpected error in file data processing: environment not found when updating", "envID", ae.Params.EnvID)
		return
	}
	synchronizer := a.envSynchronizers[ae.Params.EnvID]
	if synchronizer == nil { // COVERAGE: this should never happen and can't be covered in unit tests
		a.r.logger.Error("unexpected error in file data processing: environment not found in envUpdates", "envID", ae.Params.EnvID)
		return
	}

	env.SetIdentifiers(ae.Params.Identifiers)
	// Refresh identifier-based indexes after identifiers change
	a.r.envsByCredential.RefreshEnvironmentIndexes(env)

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
			a.r.logger.Error("error updating offline environment data", "error", err)
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
