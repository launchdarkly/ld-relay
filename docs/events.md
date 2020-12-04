# LaunchDarkly Relay Proxy - Event forwarding

[(Back to README)](../README.md)

You can use the Relay Proxy to forward analytics events from SDKs to `events.launchdarkly.com`. Alternatively, you can specify a different URL to forward the events to a different destination.

One use case for this is PHP environments, where the performance of a local proxy makes it possible to synchronously flush analytics events, but you can use it with any SDK as long as you configure the SDK to send events to the Relay Proxy. The setting for this is usually called `eventsUri`.

To enable event forwarding, follow one of these examples:

```
# Configuration file example

[Events]
    sendEvents = true
    flushInterval = 5s
    capacity = 1000
```

```
# Environment variables example

USE_EVENTS=true
EVENTS_FLUSH_INTERVAL=5s
EVENTS_CAPACITY=1000
```

This configuration buffers events for all environments specified in the configuration. The events are flushed every `flushIntervalSecs`. 

To point our SDKs to the Relay Proxy for event forwarding, set the `eventsUri` in the SDK to the host and port of your relay instance, or the host and port of a load balancer fronting your relay instances. Setting `inlineUsers` to `true` preserves full user details in every event. The default is to send them only once per user in an `"index"` event.

## Events in offline mode

In [offline mode](https://docs.launchdarkly.com/home/advanced/relay-proxy-enterprise/offline), the Relay Proxy will never send events to LaunchDarkly. However, you can still set `sendEvents = true` (or `USE_EVENTS=true` if you are using environment variables) to make the Relay Proxy accept events from SDK clients. The events will be discarded. The purpose of this behavior is to allow you to use the same SDK configuration regardless of whether the Relay Proxy is in offline mode or not, so if the SDKs are configured to send events, they can do so without getting errors.
