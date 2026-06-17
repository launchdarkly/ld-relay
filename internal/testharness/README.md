# Test Harness — Phase 1 Integration Infrastructure

Package `testharness` provides the three reusable components for Phase 1 (concurrent SDK
keys) integration testing. Tests for individual features accumulate here as Phase 1
sub-tasks land.

---

## Components

### 1. RACMock

An SSE test server that emulates LaunchDarkly's Relay Auto Config (RAC) endpoint. Use
it when testing relay's behavior under auto-config mode without a real LaunchDarkly
connection.

**Setup**: point `config.Main.StreamURI` at `racMock.URL`.

```go
putEvent := testharness.MakePutEvent(myEnvRep)
racMock := testharness.NewRACMock(t, &putEvent)   // initial event replayed to every new client

// Configure relay to use the mock
cfg := config.Config{AutoConfig: config.AutoConfigConfig{Key: testAutoConfKey}}
cfg.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(racMock.URL)
```

**Emitting events at runtime**:

```go
// Deliver an update while relay is connected
racMock.Send(testharness.MakePatchEvent(updatedEnv))

// Queue an event so it's delivered on the next (re)connect
racMock.Enqueue(testharness.MakePutEvent(env1, env2))

// Delete an environment
racMock.Send(testharness.MakeDeleteEvent(envID, version+1))
```

**Cleanup**: `NewRACMock` registers `t.Cleanup`; call `racMock.Close()` only for early
teardown.

---

### 2. SDKSimulator

Simulates a downstream SDK client connecting to relay's server-side SSE stream (`/all`).
It establishes a real HTTP connection and provides helpers to assert on received events.

```go
server := httptest.NewServer(relay)
defer server.Close()

sim := testharness.NewSDKSimulator(t, server, sdkKey)

// Wait for any event
event := sim.AwaitEvent(2 * time.Second)

// Wait for a specific event type (fails the test on timeout)
putEvent := sim.AwaitEventOfType(t, "put", 5*time.Second)
assert.Contains(t, putEvent.Data(), "my-flag-key")
```

The simulator uses the same `Authorization` header format relay expects (`sdkKey` value
passed verbatim). It buffers events in a channel, so events that arrive before
`AwaitEvent` is called are not lost.

**Cleanup**: `NewSDKSimulator` registers `t.Cleanup`; call `sim.Close()` for early
teardown.

---

### 3. ArchiveFixtureBuilder

Builds offline-mode archive files (`.tar.gz`) that relay reads via its `FileDataSource`
config. The archive format matches what `internal/filedata.ArchiveManager` expects:
an `{envID}.json` metadata file, an `{envID}-data.json` flag/segment data file, and a
`checksum.md5` file.

```go
archivePath := testharness.NewArchiveFixtureBuilder().
    AddEnv(testharness.ArchiveEnvSpec{
        Rep: envfactory.EnvironmentRep{
            EnvID:   config.EnvironmentID("my-env"),
            EnvKey:  "production",
            EnvName: "Production",
            ProjKey: "my-proj",
            ProjName: "My Project",
            SDKKey:  envfactory.SDKKeyRep{Value: config.SDKKey("sdk-...")},
            Version: 1,
        },
        DataID: "v1",
        Flags: map[string]interface{}{
            "my-flag": ldbuilders.NewFlagBuilder("my-flag").Version(1).On(true).Build(),
        },
    }).
    WriteTempFile(t)  // temp file cleaned up when the test ends

cfg.OfflineMode.FileDataSource = archivePath
```

Multiple environments can be added with repeated `AddEnv` calls (chained). Both `Flags`
and `Segments` are optional.

---

## Reference Test

`relay/concurrent_keys_harness_ref_test.go` is the canonical end-to-end test
demonstrating all three components. It has two sub-tests:

1. **Archive fixture + SDK simulator** — starts relay in offline mode with an archive
   containing a single environment and one boolean flag, then verifies an SDK simulator
   receives a `put` event with that flag via relay's `/all` SSE stream.

2. **RAC mock + SDK simulator** — configures relay with a RAC mock that emits a `put`
   event, then verifies an SDK simulator can connect to relay's SSE stream after the
   environment is created.

Run it:

```sh
go test ./relay/ -run TestConcurrentKeysHarnessReference -v
```

---

## Adding New Scenarios

Scenario tests for individual features land in the sub-task that introduces the feature
(T1.c, T2.c, T3.c, T4). Typical pattern:

```go
func TestMyFeatureScenario(t *testing.T) {
    // Set up harness...
    putEvent := testharness.MakePutEvent(myEnv)
    racMock := testharness.NewRACMock(t, &putEvent)
    // ...start relay, connect simulator, assert behavior...
    racMock.Send(testharness.MakePatchEvent(updatedEnv))
    sim.AwaitEventOfType(t, "patch", 2*time.Second)
}
```

Keep scenario tests next to the code they verify (e.g. credential rotation tests in
`relay/autoconfig_actions_test.go`), not in this package itself.
