package relay

import (
	"time"

	"github.com/launchdarkly/ld-relay/v7/internal/envfactory"

	"github.com/launchdarkly/ld-relay/v7/config"
	"github.com/launchdarkly/ld-relay/v7/internal/filedata"

	ld "github.com/launchdarkly/go-server-sdk/v6"
	"github.com/launchdarkly/go-server-sdk/v6/interfaces"
	"github.com/launchdarkly/go-server-sdk/v6/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems"
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
	r          *Relay
	envUpdates map[config.EnvironmentID]subsystems.DataSourceUpdateSink
}

type dataSourceFactoryToCaptureUpdates struct {
	updatesCh chan<- subsystems.DataSourceUpdateSink
}

type stubDataSourceToCaptureUpdates struct {
	dataSourceUpdates subsystems.DataSourceUpdateSink
	updatesCh         chan<- subsystems.DataSourceUpdateSink
}

func (a *relayFileDataActions) AddEnvironment(ae filedata.ArchiveEnvironment) {
	updatesCh := make(chan subsystems.DataSourceUpdateSink)
	transformConfig := func(baseConfig ld.Config) ld.Config {
		config := baseConfig
		config.DataSource = dataSourceFactoryToCaptureUpdates{updatesCh}
		config.Events = ldcomponents.NoEvents()
		return config
	}
	envConfig := envfactory.NewEnvConfigFactoryForOfflineMode(a.r.config.OfflineMode).MakeEnvironmentConfig(ae.Params)
	_, _, err := a.r.addEnvironment(ae.Params.Identifiers, envConfig, transformConfig)
	if err != nil {
		a.r.loggers.Errorf(logMsgAutoConfEnvInitError, ae.Params.Identifiers.GetDisplayName(), err)
		return
	}
	select {
	case updates := <-updatesCh:
		if a.envUpdates == nil {
			a.envUpdates = make(map[config.EnvironmentID]subsystems.DataSourceUpdateSink)
		}
		a.envUpdates[ae.Params.EnvID] = updates
		updates.Init(ae.SDKData)
		updates.UpdateStatus(interfaces.DataSourceStateValid, interfaces.DataSourceErrorInfo{})
	case <-time.After(time.Second * 2):
		a.r.loggers.Errorf(logMsgOfflineEnvTimeoutError, ae.Params.Identifiers.GetDisplayName())
	}
}

func (a *relayFileDataActions) UpdateEnvironment(ae filedata.ArchiveEnvironment) {
	env, _ := a.r.getEnvironment(ae.Params.EnvID)
	if env == nil { // COVERAGE: this should never happen and can't be covered in unit tests
		a.r.loggers.Errorf(logMsgInternalErrorUpdatedEnvNotFound, ae.Params.EnvID)
		return
	}
	updates := a.envUpdates[ae.Params.EnvID]
	if updates == nil { // COVERAGE: this should never happen and can't be covered in unit tests
		a.r.loggers.Errorf(logMsgInternalErrorNoUpdatesForEnv, ae.Params.EnvID)
		return
	}

	env.SetIdentifiers(ae.Params.Identifiers)
	env.SetTTL(ae.Params.TTL)
	env.SetSecureMode(ae.Params.SecureMode)

	// SDKData will be non-nil only if the flag/segment data for the environment has actually changed.
	if ae.SDKData != nil {
		updates.Init(ae.SDKData)
	}
}

func (a *relayFileDataActions) EnvironmentFailed(id config.EnvironmentID, err error) {
	// error logging goes here
}

func (a *relayFileDataActions) DeleteEnvironment(id config.EnvironmentID) {
	env, _ := a.r.getEnvironment(id)
	if env != nil {
		a.r.removeEnvironment(env)
		delete(a.envUpdates, id)
	}
}

func (d dataSourceFactoryToCaptureUpdates) Build(
	ctx subsystems.ClientContext,
) (subsystems.DataSource, error) {
	return stubDataSourceToCaptureUpdates{ctx.GetDataSourceUpdateSink(), d.updatesCh}, nil
}

func (s stubDataSourceToCaptureUpdates) Close() error {
	return nil
}

func (s stubDataSourceToCaptureUpdates) IsInitialized() bool {
	return true
}

func (s stubDataSourceToCaptureUpdates) Start(readyCh chan<- struct{}) {
	s.updatesCh <- s.dataSourceUpdates
	close(readyCh)
}
