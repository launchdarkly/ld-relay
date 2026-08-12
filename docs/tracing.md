# LaunchDarkly Relay Proxy - Tracing

[(Back to README)](../README.md)

The Relay Proxy can export distributed traces via [OpenTelemetry Protocol (OTLP)](https://opentelemetry.io/docs/specs/otlp/) to any compatible backend, such as Grafana Tempo, Jaeger, Datadog, or an OpenTelemetry Collector. Tracing is enabled by the same setting as metrics. To learn about configuration, read [Configuration](./configuration.md).

Traces and metrics share the resource attributes described in [Metrics](./metrics.md#resource-attributes), and the environment attribute is spelled the same way in both, so a trace can be correlated with a metric series for the same environment.

## The request span

Every request produces one span from the HTTP instrumentation, named after the method and the matched
route -- `GET /sdk/poll`, `REPORT /sdk/evalx/context`. It carries the standard HTTP server attributes,
including `http.route`, `http.request.method`, `http.response.status_code`, `url.path`, `url.scheme`,
`server.address`, `client.address`, `user_agent.original`, and `network.protocol.version`.

Streaming endpoints hold their connection open, so an SSE request span lasts as long as the client
stays connected.

### Redaction

On the endpoints that take an evaluation context in the URL, that segment of `url.path` holds end-user
data: context keys, names, emails, and any custom attributes the SDK sent. Relay replaces **that
segment only** with the placeholder `REDACTED`:

```
request  GET /sdk/evalx/507f1f77bcf86cd799439011/contexts/eyJrZXkiOiJ1c2VyLTEyMyJ9
url.path     /sdk/evalx/507f1f77bcf86cd799439011/contexts/REDACTED
http.route   /sdk/evalx/{envId}/contexts/{context}
```

The rest of the path is preserved, so a trace still shows which endpoint and which environment served
the request. Environment IDs are not sensitive: the Relay Proxy already returns them in the `X-LD-EnvId`
response header, in the status endpoint, and in log prefixes.

A context supplied in a `REPORT` body is never recorded on a span at all.

## Relay spans

These hang off the request span. Which ones appear depends on the endpoint: a polling request produces
a store read, a serialize, and a write; an SSE connect produces an auth span and the stream's own
activity.

| Span | What it covers |
|------|----------------|
| `relay.auth` | Credential lookup and environment selection, before the handler runs |
| `relay.store.snapshot` | Reading a consistent snapshot of the environment's data |
| `relay.store.get_all` | Reading every item of one kind from the store |
| `relay.store.get` | Reading a single flag or segment |
| `relay.flags.evaluate` | Evaluating flags against an evaluation context |
| `relay.payload.serialize` | Building the response body |
| `relay.response.write` | Writing the response body to the client |
| `relay.events.dispatch` | Handing a received analytics or diagnostic event batch to the publisher |
| `relay.singleflight.wait` | A request waiting for a payload another request was already building |

Span names are short and unprefixed on purpose: they are display strings in a trace waterfall, not
queryable dimensions, and the semantic conventions do not namespace them either.

A failure sets the span's status to `Error`. Where there is an underlying error value -- a store read
that failed, a response that could not be written -- it is also recorded as a span event carrying the
message. A rejected `relay.auth` has no such value: the reason is the
`launchdarkly.relay.auth.result` attribute.

## Span attributes

Attribute keys are the queryable dimensions, so anything not defined by a semantic convention is
prefixed with `launchdarkly.`. Keys describing the caller are `launchdarkly.<thing>`; keys describing
the Relay Proxy's own work are `launchdarkly.relay.<thing>`.

| Attribute | On | Description |
|-----------|-----|-------------|
| `launchdarkly.sdk.kind` | `relay.auth` | The category of SDK that authenticated: `server`, `mobile`, or `js` |
| `launchdarkly.relay.auth.result` | `relay.auth` | `success`, `invalid_credential`, `not_ready`, `filter_not_found`, `not_found`, or `client_not_initialized` |
| `launchdarkly.relay.store.key` | `relay.store.get` | The flag or segment key being read |
| `launchdarkly.relay.flag.count` | `relay.flags.evaluate`, `relay.payload.serialize` | Number of flags in the evaluation or the payload |
| `launchdarkly.relay.payload.event.count` | `relay.payload.serialize` | Number of protocol events in a payload |
| `launchdarkly.relay.payload.size` | `relay.payload.serialize` | Size of the serialized payload, in bytes |
| `launchdarkly.relay.events.kind` | `relay.events.dispatch` | The kind of event batch received |
| `http.response.status_code` | `relay.response.write` | The status written to the client |

`launchdarkly.relay.payload.size` is what the Relay Proxy *built*. What actually went out on the wire
is `http.response.body.size` on the request span, counted outside the compression middleware, and the
two differ whenever a response is compressed.

### Request deduplication

Concurrent requests that would build the same payload share one build. Both attributes below land on
the **request** span:

| Attribute | Description |
|-----------|-------------|
| `launchdarkly.relay.singleflight.shared` | Whether this payload build was shared with other in-flight requests |
| `launchdarkly.relay.singleflight.wait.duration` | How long this request waited for another request's build, in seconds. Present only on a request that waited |

The request that performed the build carries no wait attribute -- it did not wait, and its work appears
as the child spans in its own trace. So a trace with `shared=true`, no wait attribute, and no store or
serialize children means another request's trace holds the build. Tracing backends cannot join a
waiting request to the trace that did the work; find it by timestamp proximity.

## Instrumentation scope

Spans the Relay Proxy creates itself report the scope name `ld-relay`. Spans created by the HTTP
instrumentation report that library's own scope name, so the two can be told apart.
