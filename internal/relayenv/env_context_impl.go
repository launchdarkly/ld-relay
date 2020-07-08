package relayenv

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/internal/store"
	"github.com/launchdarkly/ld-relay/v6/sdkconfig"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
)

type envContextImpl struct {
	mu           sync.RWMutex
	client       sdkconfig.LDClientContext
	storeAdapter *store.SSERelayDataStoreAdapter
	loggers      ldlog.Loggers
	handlers     ClientHandlers
	credentials  Credentials
	name         string
	metricsEnv   *metrics.EnvironmentManager
	ttl          time.Duration
	initErr      error
}

// NewEnvContext creates the internal implementation of EnvContext.
//
// It immediately begins trying to initialize the SDK client for this environment. Since that might
// take a while, it is done on a separate goroutine. The EnvContext instance is returned immediately
// in an uninitialized state, and once the SDK client initialization has either succeeded or failed,
// the same EnvContext will be pushed to the channel readyCh.
//
// NewEnvContext can also immediately return an error, with a nil EnvContext, if the configuration is
// invalid.
func NewEnvContext(
	envName string,
	envConfig config.EnvConfig,
	allConfig config.Config,
	clientFactory sdkconfig.ClientFactoryFunc,
	dataStoreFactory interfaces.DataStoreFactory,
	allPublisher, flagsPublisher, pingPublisher *eventsource.Server,
	metricsManager *metrics.Manager,
	loggers ldlog.Loggers,
	readyCh chan<- EnvContext,
) (EnvContext, error) {
	envLoggers := loggers
	envLoggers.SetPrefix(fmt.Sprintf("[env: %s]", envName))
	if envConfig.LogLevel != "" {
		envLoggers.SetMinLevel(envConfig.GetLogLevel())
	}

	httpConfig, err := httpconfig.NewHTTPConfig(allConfig.Proxy, envConfig.SdkKey, loggers)
	if err != nil {
		return nil, err
	}

	clientConfig := ld.Config{
		DataSource: ldcomponents.StreamingDataSource().BaseURI(allConfig.Main.StreamUri),
		HTTP:       httpConfig.SDKHTTPConfigFactory,
		Logging:    ldcomponents.Logging().Loggers(envLoggers),
	}

	storeAdapter := store.NewSSERelayDataStoreAdapter(dataStoreFactory,
		store.SSERelayDataStoreParams{
			SDKKey:            envConfig.SdkKey,
			AllPublisher:      allPublisher,
			FlagsPublisher:    flagsPublisher,
			PingPublisher:     pingPublisher,
			HeartbeatInterval: allConfig.Main.HeartbeatIntervalSecs,
		})
	clientConfig.DataStore = storeAdapter

	var eventDispatcher *events.EventDispatcher
	if allConfig.Events.SendEvents {
		envLoggers.Info("Proxying events for this environment")
		eventDispatcher = events.NewEventDispatcher(envConfig.SdkKey, envConfig.MobileKey, envConfig.EnvId,
			envLoggers, allConfig.Events, httpConfig, storeAdapter)
	}

	eventsPublisher, err := events.NewHttpEventPublisher(envConfig.SdkKey, envLoggers,
		events.OptionUri(allConfig.Events.EventsUri),
		events.OptionClient{Client: httpConfig.Client()})
	if err != nil {
		return nil, fmt.Errorf("unable to create publisher: %s", err)
	}

	var em *metrics.EnvironmentManager
	if metricsManager != nil {
		em, err = metricsManager.AddEnvironment(envName, eventsPublisher)
		if err != nil {
			return nil, fmt.Errorf("unable to create metrics processor: %s", err)
		}
	}

	envContext := &envContextImpl{
		name: envName,
		credentials: Credentials{
			SDKKey:        envConfig.SdkKey,
			MobileKey:     ldvalue.NewOptionalStringFromPointer(envConfig.MobileKey).OnlyIfNonEmptyString(),
			EnvironmentID: ldvalue.NewOptionalStringFromPointer(envConfig.EnvId).OnlyIfNonEmptyString(),
		},
		storeAdapter: storeAdapter,
		loggers:      envLoggers,
		metricsEnv:   em,
		ttl:          time.Minute * time.Duration(envConfig.TtlMinutes),
		handlers: ClientHandlers{
			EventDispatcher:    eventDispatcher,
			AllStreamHandler:   allPublisher.Handler(envConfig.SdkKey),
			FlagsStreamHandler: flagsPublisher.Handler(envConfig.SdkKey),
			PingStreamHandler:  pingPublisher.Handler(envConfig.SdkKey),
		},
	}

	// Connecting may take time, so do this in parallel
	go func(envName string, envConfig config.EnvConfig) {
		client, err := clientFactory(envConfig.SdkKey, clientConfig)
		envContext.SetClient(client)

		if err != nil {
			envContext.initErr = err
			if !allConfig.Main.IgnoreConnectionErrors {
				envLoggers.Errorf("Error initializing LaunchDarkly client for %s: %+v\n", envName, err)

				if allConfig.Main.ExitOnError {
					os.Exit(1)
				}
				if readyCh != nil {
					readyCh <- envContext
				}
				return
			}

			loggers.Errorf("Ignoring error initializing LaunchDarkly client for %s: %+v\n", envName, err)
		} else {
			loggers.Infof("Initialized LaunchDarkly client for %s\n", envName)
		}
		if readyCh != nil {
			readyCh <- envContext
		}
	}(envName, envConfig)

	return envContext, nil
}

func (c *envContextImpl) GetName() string {
	return c.name
}

func (c *envContextImpl) GetCredentials() Credentials {
	return c.credentials
}

func (c *envContextImpl) GetClient() sdkconfig.LDClientContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client
}

func (c *envContextImpl) SetClient(client sdkconfig.LDClientContext) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.client = client
}

func (c *envContextImpl) GetStore() interfaces.DataStore {
	if c.storeAdapter == nil {
		return nil
	}
	return c.storeAdapter.GetStore()
}

func (c *envContextImpl) GetLoggers() ldlog.Loggers {
	return c.loggers
}

func (c *envContextImpl) GetHandlers() ClientHandlers {
	return c.handlers
}

func (c *envContextImpl) GetMetricsContext() context.Context {
	if c.metricsEnv == nil {
		return context.Background()
	}
	return c.metricsEnv.GetOpenCensusContext()
}

func (c *envContextImpl) GetTTL() time.Duration {
	return c.ttl
}

func (c *envContextImpl) GetInitError() error {
	return c.initErr
}
