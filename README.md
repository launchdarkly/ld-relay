# LaunchDarkly Relay Proxy

[![Actions Status](https://github.com/launchdarkly/ld-relay/actions/workflows/ci.yml/badge.svg?branch=v7)](https://github.com/launchdarkly/ld-relay/actions/workflows/ci.yml)
[![Actions Status](https://github.com/launchdarkly/ld-relay/actions/workflows/daily-integration-tests.yml?branch=v7/badge.svg)](https://github.com/launchdarkly/ld-relay/actions/workflows/daily-integration-tests.yml)
[![Actions Status](https://github.com/launchdarkly/ld-relay/actions/workflows/daily-security-scan.yml/badge.svg?branch=v7)](https://github.com/launchdarkly/ld-relay/actions/workflows/daily-security-scan.yml)
[![Actions Status](https://github.com/launchdarkly/ld-relay/actions/workflows/daily-installation-test.yml/badge.svg?branch=v7)](https://github.com/launchdarkly/ld-relay/actions/workflows/daily-installation-test.yml)

## About the LaunchDarkly Relay Proxy

The LaunchDarkly Relay Proxy establishes a connection to the LaunchDarkly streaming API, then proxies that stream connection to multiple clients. It lets a number of servers connect to a local stream instead of making a large number of outbound connections to `stream.launchdarkly.com`.

You can configure the Relay Proxy to proxy multiple environment streams, even across multiple projects. You can also use it as a local proxy that forwards events to `events.launchdarkly.com`. This can be useful if you are load balancing Relay Proxy instances behind a proxy that times out HTTP connections, such as Elastic Load Balancers.

To learn more, read [The Relay Proxy](https://docs.launchdarkly.com/home/relay-proxy).


## When to use the LaunchDarkly Relay Proxy

To learn more about appropriate use cases for the Relay Proxy, read [Determining whether to use the Relay Proxy](https://docs.launchdarkly.com/home/relay-proxy/determining).


## Getting started

To learn more about setting up the Relay Proxy, read [Implementing the Relay Proxy](https://docs.launchdarkly.com/home/relay-proxy/implementing).


## Capabilities

SDKs can connect to the Relay Proxy in one of two modes: proxy mode or daemon mode. To learn more, read [Configuring an SDK to use different modes](https://docs.launchdarkly.com/home/relay-proxy/using#configuring-an-sdk-to-use-different-modes).

Here are the differences between the two modes:

- In proxy mode, the Relay Proxy simulates the LaunchDarkly service endpoints that LaunchDarkly SDKs use. The SDKs can connect to the Relay Proxy as if it were LaunchDarkly. To learn about proxy mode configuration, read [Proxy mode](./docs/proxy-mode.md).
- In daemon mode, the Relay Proxy puts feature flag data into a database and the SDKs use that database instead of making HTTP requests. To learn about daemon mode configuration, read [Daemon mode](./docs/daemon-mode.md).

If you provide a mobile key and/or a client-side environment ID in the [configuration](./docs/configuration.md#file-section-environment-name) for an environment, the Relay Proxy can also accept connections from mobile clients and/or JavaScript clients. To learn more, read [Client-side and mobile connections](./docs/client-side.md).

If you enable event forwarding in the [configuration](./docs/configuration.md#file-section-events), the Relay Proxy accepts analytics events from SDKs and forwards them to LaunchDarkly. To learn more, read [Event forwarding](./docs/events.md).

There are some special considerations if you use the PHP SDK. To learn more, read [Using PHP](./docs/php.md).


## Enterprise capabilities

LaunchDarkly offers additional Relay Proxy features to customers on Enterprise plans: automatic configuration and offline mode.

### Automatic configuration

Automatic configuration automatically detects when you create and update environments, removing the need for most manual configuration file changes and application restarts. Instead, you can use a simple in-app UI to manage your Relay Proxy configuration. To learn more, read [Automatic configuration](https://docs.launchdarkly.com/home/advanced/relay-proxy-enterprise/automatic-configuration).

### Offline mode

You can run the Relay Proxy in online mode or offline mode. When running in offline mode, the Relay Proxy gets flag and segment values from an archive on your filesystem, instead of contacting LaunchDarkly's servers. 

To run the Relay Proxy in offline mode, your SDKs must be configured for proxy mode. To learn more, read [Offline mode](https://docs.launchdarkly.com/home/advanced/relay-proxy-enterprise/offline).

If you want access to these features but don’t have a LaunchDarkly Enterprise plan, [contact our sales team](https://launchdarkly.com/contact-sales/).


## Specifying a configuration

There are many configuration options, which can be specified in a file, in environment variables, or both. To learn more, read [Configuration](./docs/configuration.md).


## Deployment options

There are several ways to deploy the Relay Proxy.

In order from most common to least common uses, the methods are:

- [starting the Relay Proxy from a Docker image](./docs/docker.md),
- [using Helm to run the Relay Proxy in Kubernetes](https://github.com/launchdarkly/ld-relay-helm),
- downloading a Relay Proxy standalone release binary, [such as in Windows](./docs/windows.md), or
- [building within an existing Go app](./docs/in-app.md).

To learn more, read [Deploying the Relay Proxy](https://docs.launchdarkly.com/home/relay-proxy/deploying#deploying-the-relay-proxy).

## Command-line arguments

| Argument               | Description                                                              |
|------------------------|--------------------------------------------------------------------------|
| `--config FILEPATH`    | configuration file location                                              |
| `--allow-missing-file` | if specified, a `--config` option for a nonexistent file will be ignored |
| `--from-env`           | if specified, configuration will be read from environment variables      |
| `--version`            | if specified, print relay's version and stop execution                   |

If none of these are specified, the default is `--config /etc/ld-relay.conf`.


## Persistent storage

You can configure Relay Proxy nodes to persist feature flag settings in Redis, DynamoDB, or Consul. You must use persistent storage to run your SDKs in daemon mode. To learn more, read [Using a persistent store](https://docs.launchdarkly.com/home/relay-proxy/using#using-a-persistent-store).

You can also configure the Relay Proxy to persist segment information for Big Segments in Redis or DynamoDB. To learn more, read [Configuring the Relay Proxy for segments](https://docs.launchdarkly.com/home/relay-proxy/using#configuring-the-relay-proxy-for-segments).

> Segments let you target groups of contexts that encounter feature flags. Big Segments are segments with more than 15,000 targets, or that are synced from external tools. You must use either the Relay Proxy or a persistent store integration if you use server-side SDKs and Big Segments. If supporting segments is your only use case, we recommend using a persistent store integration rather than the Relay Proxy.

For persistent storage configuration details, read [Persistent Storage](./docs/persistent-storage.md).


## Exporting metrics and traces

The Relay Proxy may be configured to export statistics and route traces to Datadog, Stackdriver, and Prometheus. To learn more, read [Metrics integrations](./docs/metrics.md).


## Logging

To learn about Relay Proxy logging, read [Logging](./docs/logging.md).


## Service endpoints

The Relay Proxy defines many HTTP/HTTPS endpoints. Most of these are proxies for LaunchDarkly services, to be used by SDKs that connect to the Relay Proxy. Others are specific to the Relay Proxy, such as for monitoring its status.

To learn more, read [Service endpoints](./docs/endpoints.md).


## Performance, scaling, and operations

We have done extensive load tests on the Relay Proxy in AWS/EC2. We have also collected a substantial amount of data based on real-world customer use. Based on our experience, we have several recommendations on how to best deploy, operate, and scale the Relay Proxy:

* Networking performance is the most important consideration. Memory and CPU are not as critical. Deploy the Relay Proxy on boxes with good networking performance. On EC2, we recommend using an instance with [Moderate to High networking performance](http://www.ec2instances.info/) such as `m4.xlarge`. On an `m4.xlarge` instance, a single Relay Proxy node can easily manage 20,000 concurrent connections.

* If you use an Elastic Load Balancer in front of the Relay Proxy, you may need to [pre-warm](https://aws.amazon.com/articles/1636185810492479) the load balancer whenever connections to the Relay Proxy cycle. This might happen when you deploy a large number of new servers that connect to the Relay Proxy, or upgrade the Relay Proxy itself.

To learn more, read [Testing Relay Proxy performance](https://docs.launchdarkly.com/home/advanced/relay-proxy/performance).


## Contributing

We encourage pull requests and other contributions from the community. For instructions on how to contribute to this project, read our [contributing guidelines](CONTRIBUTING.md).


## About LaunchDarkly

* LaunchDarkly is a continuous delivery platform that provides feature flags as a service and allows developers to iterate quickly and safely. We allow you to easily flag your features and manage them from the LaunchDarkly dashboard.  With LaunchDarkly, you can:
    * Roll out a new feature to a subset of your users (like a group of users who opt-in to a beta tester group), gathering feedback and bug reports from real-world use cases.
    * Gradually roll out a feature to an increasing percentage of users, and track the effect that the feature has on key metrics (for instance, how likely is a user to complete a purchase if they have feature A versus feature B?).
    * Turn off a feature that you realize is causing performance problems in production, without needing to re-deploy, or even restart the application with a changed configuration file.
    * Grant access to certain features based on user attributes, like payment plan (eg: users on the ‘gold’ plan get access to more features than users in the ‘silver’ plan). Disable parts of your application to facilitate maintenance, without taking everything offline.
* LaunchDarkly provides feature flag SDKs for a wide variety of languages and technologies. For a complete list, read [our documentation](https://docs.launchdarkly.com/docs).
* Explore LaunchDarkly
    * [launchdarkly.com](https://www.launchdarkly.com/ "LaunchDarkly Main Website") for more information
    * [docs.launchdarkly.com](https://docs.launchdarkly.com/  "LaunchDarkly Documentation") for our documentation and SDK reference guides
    * [apidocs.launchdarkly.com](https://apidocs.launchdarkly.com/  "LaunchDarkly API Documentation") for our API documentation
    * [blog.launchdarkly.com](https://blog.launchdarkly.com/  "LaunchDarkly Blog Documentation") for the latest product updates
    * [Feature Flagging Guide](https://github.com/launchdarkly/featureflags/  "Feature Flagging Guide") for best practices and strategies
