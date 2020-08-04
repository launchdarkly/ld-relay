# LaunchDarkly Relay Proxy - Logging

[(Back to README)](../README.md)

Like the Go SDK, the Relay Proxy supports four logging levels: 

* Debug (most verbose), 
* Info (default), 
* Warn, and 
* Error. 

If you set the logging level to Warn, Info and Debug are disabled. Similarly, if you set the logging level to Info, Debug is disabled. 

There are two categories of log output: global messages and per-environment messages.

## Global logging

Global messages are from the general Relay Proxy infrastructure. For example, logging when the Relay Proxy has successfully started up.

The minimum enabled log level for global messages is controlled in [your configuration](./configuration.md#file-section-man) by the `[Main] logLevel` parameter, or the `LOG_LEVEL` environment variable. The default is Info.

## Per-environment logging

Per-environment messages are for the Relay Proxy's interaction with LaunchDarkly for a specific one of your configured environments. For example, receiving a flag update or sending analytics events. 

These messages mostly come from the LaunchDarkly Go SDK, which the Relay Proxy uses to communicate with LaunchDarkly.

The minimum enabled log level for per-environment messages is controlled in [your configuration](./configuration.md#file-section-environment-name) by the `[Environment "envname"] logLevel` parameter, or the `LD_LOG_LEVEL_envname` environment variable. You can set this separately for each environment. 

If you don't specify a log level for an environment, it uses the same log level that was specified for global messages. You can control these messages separately in the [configuration](./configuration.md). For instance, you may wish to see more verbose output in one environment than another, or enable Debug logging globally for HTTP requests without enabling it for per-environment messages.

## Debug logging

Enabling the Debug log level for global messages causes the Relay Proxy to log every HTTP request that it receives.

For per-environment messages, Debug logging includes verbose information about the operation of the Go SDK, which this may include user properties and feature flag keys. You will normally not want to enable this output, so if you have set the global level to Debug to log HTTP requests, you should set it to something other than Debug for your environments.
