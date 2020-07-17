# LaunchDarkly Relay Proxy - Event Forwarding

[(back to README)](../README.md)

The Relay Proxy can also be used to forward analytics events from SDKs to `events.launchdarkly.com` (unless you have specified a different URL for the events service in your configuration).

One use case for this is PHP environments, where the performance of a local proxy makes it possible to synchronously flush analytics events, but it can be used with any SDK as long as you configure the SDK to send events to the Relay Proxy (the setting for this is usually called `eventsUri`).

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

This configuration will buffer events for all environments specified in the configuration. The events will be flushed every `flushIntervalSecs`. To point our SDKs to the Relay Proxy for event forwarding, set the `eventsUri` in the SDK to the host and port of your relay instance (or preferably, the host and port of a load balancer fronting your relay instances). Setting `inlineUsers` to `true` preserves full user details in every event (the default is to send them only once per user in an `"index"` event).
