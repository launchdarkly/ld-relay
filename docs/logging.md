# LaunchDarkly Relay Proxy - Logging

[(back to README)](../README.md)

Like the Go SDK, the Relay Proxy supports four logging levels: Debug, Info, Warn, and Error, with Debug being the most verbose. Setting the minimum level to Info (the default) means Debug is disabled; setting it to Warn means Debug and Info are disabled; etc.

There are two categories of log output: global messages and per-environment messages.

## Global logging

Global messages are from the general Relay Proxy infrastructure: for instance, logging when the Relay Proxy has successfully started up.

The minimum enabled log level for global messages is controlled in [your configuration](./configuration.md#file-section-man) by the `[Main] logLevel` parameter, or the `LOG_LEVEL` environment variable. The default is Info.

## Per-environment logging

Per-environment messages are for the Relay Proxy's interaction with LaunchDarkly for a specific one of your configured environments: for instance, receiving a flag update or sending analytics events. These messages mostly come from the LaunchDarkly Go SDK, which the Relay Proxy uses to communicate with LaunchDarkly.

The minimum enabled log level for per-environment messages is controlled in [your configuration](./configuration.md#file-section-environment-name) by the `[Environment "envname"] logLevel` parameter, or the `LD_LOG_LEVEL_envname` environment variable. You can set this separately for each environment. If you don't specify a log level for an environment, it uses the same log level that was specified for global messages.


These can be controlled separately in the [configuration](./configuration.md). For instance, you may wish to see more verbose output in one environment than another, or enable Debug logging globally for HTTP requests without enabling it for per-environment messages.

## Debug logging

Enabling the Debug log level for global messages causes the Relay Proxy to log every HTTP request that it receives.

For per-environment messages, Debug logging includes verbose information about the operation of the Go SDK, which this may include user properties and feature flag keys. You will normally not want to enable this output, so if you have set the global level to Debug to log HTTP requests, you should set it to something other than Debug for your environments.
