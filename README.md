# LaunchDarkly Relay Proxy

## What is it?

The LaunchDarkly Relay Proxy establishes a connection to the LaunchDarkly streaming API, then proxies that stream connection to multiple clients.

The Relay Proxy lets a number of servers connect to a local stream instead of making a large number of outbound connections to `stream.launchdarkly.com`.

The Relay Proxy can be configured to proxy multiple environment streams, even across multiple projects. It can also be used as a local proxy that forwards events  to `events.launchdarkly.com`. This can be useful if you are load balancing Relay Proxy instances behind a proxy that times out HTTP connections (e.g. Elastic Load Balancers).


## When should it be used?

Refer to [our documentation](https://docs.launchdarkly.com/home/advanced/relay-proxy#should-i-use-the-relay-proxy) for guidance on situations where the Relay Proxy should be used.


## Getting started

Refer to [our documentation](https://docs.launchdarkly.com/home/advanced/relay-proxy/using#starting-the-relay-proxy) for instructions on getting started with using the Relay Proxy.


## Capabilities

In the most basic configuration, the Relay Proxy simulates the LaunchDarkly service endpoints that are used by LaunchDarkly SDKs. The SDKs can connect to the Relay Proxy as if it were LaunchDarkly. **See: [Proxy Mode](./docs/proxy-mode.md)**

You can also have the Relay Proxy put feature flag data into a database, and have the SDKs use that database instead of making HTTP requests. **See: [Daemon Mode](./docs/daemon-mode.md)**

If you provide a mobile key and/or a client-side environment ID in the [configuration](./docs/configuration.md#file-section-environment-name) for an environment, the Relay Proxy will also accept connections from mobile clients and/or JavaScript clients. **See: [Client-Side/Mobile Connections](./docs/client-side.md)**

If you enable event forwarding in the [configuration](./docs/configuration.md#file-section-events), the Relay Proxy will accept analytics events from SDKs and forward them to LaunchDarkly. **See: [Event Forwarding](./docs/events.md)**

There are some special considerations if you are using the PHP SDK. **See: [Using PHP](./docs/php.md)**


## Deployment options

A common way to run the Relay Proxy is as a Docker container. **See: [Using with Docker](./docs/docker.md)**

It can also be run as a Windows service. **See: [Building and Running in Windows](./docs/windows.md)**

Or, you can build the Relay Proxy endpoints into your own application. See: **[Building Within an Application](./docs/in-app.md)**


## Command-line arguments

Argument               | Description
---------------------- | --------------------
`--config FILEPATH`    | configuration file location
`--allow-missing-file` | if specified, a `--config` option for a nonexistent file will be ignored
`--from-env`           | if specified, configuration will be read from environment variables

If none of these are specified, the default is `--config /etc/ld-relay.conf`.


## Specifying a configuration

There are many configuration options, which can be specified in a file, in environment variables, or both.

**For details, see: [Configuration](./docs/configuration.md)**


## Persistent storage

You can configure Relay Proxy nodes to persist feature flag settings in Redis, DynamoDB, or Consul.

**For details, see: [Persistent Storage](./docs/persistent-storage.md)**


## Exporting metrics and traces

The Relay Proxy may be configured to export statistics and route traces to Datadog, Stackdriver, and Prometheus.

**For details, see: [Metrics Integrations](./docs/metrics.md)**


## Logging

**For details, see: [Logging](./docs/logging.md)**


## Service endpoints

The Relay Proxy defines many HTTP/HTTPS endpoints. Most of these are proxies for LaunchDarkly services, to be used by SDKs that connect to the Relay Proxy. Others are specific to the Relay Proxy, such as for monitoring its status.

**For details, see: [Service Endpoints](./docs/endpoints.md)**


## Performance, scaling, and operations

We have done extensive load tests on the Relay Proxy in AWS/EC2. We have also collected a substantial amount of data based on real-world customer use. Based on our experience, we have several recommendations on how to best deploy, operate, and scale the Relay Proxy:

* Networking performance is paramount. Memory and CPU are not as critical. The Relay Proxy should be deployed on boxes with good networking performance. On EC2, we recommend using an instance with [Moderate to High networking performance](http://www.ec2instances.info/) such as `m4.xlarge`. On an `m4.xlarge` instance, a single Relay Proxy node can easily manage 20,000 concurrent connections.

* If using an Elastic Load Balancer in front of the Relay Proxy, you may need to [pre-warm](https://aws.amazon.com/articles/1636185810492479) the load balancer whenever connections to the Relay Proxy are cycled. This might happen when you deploy a large number of new servers that connect to the Relay Proxy, or upgrade the Relay Proxy itself.


## Contributing

We encourage pull requests and other contributions from the community. Check out our [contributing guidelines](CONTRIBUTING.md) for instructions on how to contribute to this project.


## About LaunchDarkly

* LaunchDarkly is a continuous delivery platform that provides feature flags as a service and allows developers to iterate quickly and safely. We allow you to easily flag your features and manage them from the LaunchDarkly dashboard.  With LaunchDarkly, you can:
    * Roll out a new feature to a subset of your users (like a group of users who opt-in to a beta tester group), gathering feedback and bug reports from real-world use cases.
    * Gradually roll out a feature to an increasing percentage of users, and track the effect that the feature has on key metrics (for instance, how likely is a user to complete a purchase if they have feature A versus feature B?).
    * Turn off a feature that you realize is causing performance problems in production, without needing to re-deploy, or even restart the application with a changed configuration file.
    * Grant access to certain features based on user attributes, like payment plan (eg: users on the ‘gold’ plan get access to more features than users in the ‘silver’ plan). Disable parts of your application to facilitate maintenance, without taking everything offline.
* LaunchDarkly provides feature flag SDKs for a wide variety of languages and technologies. Check out [our documentation](https://docs.launchdarkly.com/docs) for a complete list.
* Explore LaunchDarkly
    * [launchdarkly.com](https://www.launchdarkly.com/ "LaunchDarkly Main Website") for more information
    * [docs.launchdarkly.com](https://docs.launchdarkly.com/  "LaunchDarkly Documentation") for our documentation and SDK reference guides
    * [apidocs.launchdarkly.com](https://apidocs.launchdarkly.com/  "LaunchDarkly API Documentation") for our API documentation
    * [blog.launchdarkly.com](https://blog.launchdarkly.com/  "LaunchDarkly Blog Documentation") for the latest product updates
    * [Feature Flagging Guide](https://github.com/launchdarkly/featureflags/  "Feature Flagging Guide") for best practices and strategies
