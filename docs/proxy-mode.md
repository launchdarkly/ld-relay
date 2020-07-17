# LaunchDarkly Relay Proxy - Proxy Mode

[(back to README)](../README.md)

The Relay Proxy is typically deployed in proxy mode. In this mode, several Relay Proxy instances are deployed in a high-availability configuration behind a load balancer. Relay Proxy nodes do not need to communicate with each other, and there is no master or cluster. This makes it easy to scale the Relay Proxy horizontally by deploying more nodes behind the load balancer.

![Relay Proxy with load balancer](relay-lb.png)

You will need to configure your SDKs to point to the location of the Relay Proxy, so that they connect to it instead of connecting to LaunchDarkly. See the reference guide on [Configuring an SDK to use the Relay Proxy](https://docs.launchdarkly.com/home/advanced/relay-proxy/using#configuring-an-sdk-to-use-the-relay-proxy).

For a list of all of the service endpoints that Relay Proxy implements for use by the SDKs, see [Endpoints](./endpoints.md).

If you want SDKs to connect to the Relay Proxy securely, see [Using TLS](./tls.md).
