# How to Refresh the Event Payload Regression Baseline

The fixtures in `internal/events/testdata/` represent the v8 upstream event schemas.
They were captured at the start of Phase 1 (before any concurrent-keys changes) and serve
as the regression baseline for `TestEventPayloadRegression*` in `event_payload_regression_test.go`.

## When to refresh

Refresh the baseline **only** when a schema change is legitimate and intentional — for example,
a new event kind introduced by the Go SDK, or a new field added to an existing event type.
Do NOT refresh to suppress a test failure caused by an accidental Phase 1 regression.

If a regression test unexpectedly fails, investigate the root cause first:
- Did a Phase 1 change accidentally alter the event body structure?
- Did a `ReplaceCredential` call stop propagating to the EventDispatcher?
- Did the summarizing relay change its output format?

A legitimate reason to refresh looks like: "we intentionally updated the Go SDK dependency
and the new SDK version adds a `samplingRatio` field to custom events."

## How to refresh

The fixtures are plain JSON files. Update them manually to reflect the new expected schema.

### `baseline_analytics_verbatim.json`

This represents a realistic batch of schema-v4 analytics events that a modern Go SDK sends.
It is forwarded verbatim by relay's `eventVerbatimRelay`. To update:

1. Find a representative analytics event batch from a running relay's upstream traffic
   (e.g. from integration test logs or a local test run with a real SDK).
2. Replace the file contents with the new event batch.
3. Verify `TestEventPayloadRegressionVerbatimAnalytics` passes.

### `baseline_diagnostic.json`

This represents a `diagnostic-init` event payload. Relay forwards it verbatim. To update:

1. Find a representative diagnostic event from upstream traffic.
2. Replace the file contents.
3. Verify `TestEventPayloadRegressionDiagnostic` passes.

### `baseline_analytics_summarize_input.json`

This represents pre-summarization events (sent by PHP SDKs using schema ≤2 or with the
`X-LaunchDarkly-Unsummarized` header). Relay summarizes these before forwarding. To update:

1. Update the input events to match whatever format the SDK sends.
2. The test (`TestEventPayloadRegressionSummarizedAnalytics`) checks the structural invariants
   of the output (array of event objects, "kind" field present, last event is "summary") rather
   than byte-exact content, so the test logic rarely needs updating.
3. Verify `TestEventPayloadRegressionSummarizedAnalytics` passes.

## PR requirement

Any PR that refreshes these fixtures must include in its description:
- Why the schema changed (what SDK or relay change drove it)
- Confirmation that the change is intentional and reviewed

This is the same standard as modifying a golden-file test.
