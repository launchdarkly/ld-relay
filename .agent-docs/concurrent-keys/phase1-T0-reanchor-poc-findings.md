# T0 — Re-anchoring PoC: Findings

**Ticket**: [SDK-2530](https://launchdarkly.atlassian.net/browse/SDK-2530)
**Epic**: [SDK-2453](https://launchdarkly.atlassian.net/browse/SDK-2453)
**Design**: [`phase1-design.md`](./phase1-design.md) §7 "Re-anchoring"
**Tests**: [`internal/relayenv/env_context_reanchor_test.go`](../../internal/relayenv/env_context_reanchor_test.go)

## Purpose

Validate the upstream SDK-client swap mechanism that **T2.c** will implement, answering the seven
hypotheses from design §7 with durable tests *before* T2 begins. T0 validates feasibility; T2.c
implements. The tests in `env_context_reanchor_test.go` are written against today's primitives so they
survive into T2 as regression tests and as the executable spec for the swap.

There is no dedicated re-anchor method yet. The closest existing code path is `UpdateCredential` with a
grace period (rotate the primary SDK key, stand up a new client, keep the old one alive during a grace
window). Several PoC tests drive that path and observe where it falls short of the §7 requirements;
those gaps are the concrete acceptance criteria for T2.c.

## Headline conclusion

**Re-anchoring is feasible, but it is _not_ a transparent side-effect of today's code — three concrete
gaps must be closed by T2.c/T2.d, and one design assumption (the "shared store") is only true for
persistent stores.** None of the gaps are blockers; each has a clear remedy. The single highest-risk
item is the in-memory data store being rebuilt (emptied) when the new anchor client starts; the remedy is
to hand the existing store over to the new client rather than rebuild it (H5).

---

## Findings per hypothesis

### H1 — Two SDK clients sharing a `storeAdapter` don't corrupt store invariants

**Answer: No corruption, but the "shared store" is a misconception for the in-memory store.**

`SSERelayDataStoreAdapter.Build()` is called once per SDK-client creation (the SDK invokes
`DataStore.Build()` during client init). Each call constructs a **new** `streamUpdatesStoreWrapper`
around a **freshly built** underlying store and atomically swaps `adapter.store` to point at it. So:

- With the **default in-memory** store, the second client (the new anchor) gets a brand-new, empty,
  uninitialized store. Design §7's "two SDK clients … can feed the same store as a side-effect" does
  **not** hold here — the new client must re-sync from scratch.
- With a **persistent** store (Redis/DynamoDB), `wrappedFactory.Build()` returns a handle to the same
  external database, so the data (and `IsInitialized()`) survive the swap. This is the only
  configuration in which the §7 assumption is literally true.

No invariant corruption occurs in either case (the swap is atomic under the adapter's lock), but the
emptiness of the new in-memory store is the crux of H5. The remedy — handing the existing store over to
the new client rather than rebuilding — is covered under H5.

### H2 — Downstream SSE connections tolerate the swap

**Answer: Yes — open connections survive; expect one duplicate `put`.**

- **Connection survival:** downstream streams live in `envStreams`, keyed by `ScopedCredential`,
  entirely independent of the upstream `clients` map. A re-anchor touches only `clients`, the rotator
  anchor pointer, and the data store. An open client-side connection (keyed on env ID) keeps receiving
  events across the swap (verified live: a `ping` still arrives after re-anchor). Connections are torn
  down **only** for credentials that are actually removed (`removeCredential` → `RemoveCredential` →
  `Close()`), which is the intended graceful-rotation behavior, not a swap side-effect.
- **Duplicate `put`:** the new anchor client's initial sync calls `store.Init(allData)`, which flows
  through the store wrapper → `SendAllDataUpdate` → re-broadcast of a full `put`/`ping` to every
  connected downstream stream. From a downstream SDK's perspective this is a duplicate put. It is
  tolerable (SDKs apply puts idempotently) but **T2.c must expect it**; it is not corruption.

### H3 — Big-segment sync after re-anchor

**Answer: Re-wiring is required. It is NOT handled today.**

`bigSegmentSync` is constructed once in `NewEnvContext`, wired to `envConfig.SDKKey` and `envConfig.EnvID`
at construction. The PoC confirms the swap path neither recreates the synchronizer nor informs it of the
new key (the `BigSegmentSynchronizer` interface has Start / HasSynced / SegmentUpdatesCh / Close — **no
credential-replacement method**). After a re-anchor it keeps polling/streaming big-segment data on the
**old** anchor key, which will break once the old key is revoked.

**T2.d action:** add a re-wire path to `BigSegmentSynchronizer` (a `ReplaceCredential`-style method,
mirroring the event dispatcher / metrics publisher) **or** recreate the synchronizer on each re-anchor.
The "recreate" option is simpler; the "re-wire" option avoids dropping in-flight sync state.

### H4 — `httpconfig` stays functional after re-anchor

**Answer: Yes — no re-wire needed.**

`httpconfig` carries TLS / proxy / transport / user-agent configuration plus the SDK key, but the only
key-dependent artifact is the `Authorization` default header on the pre-built `SDKHTTPConfig`. Relay
injects the *builder* (`SDKHTTPConfigFactory`), not the pre-built config, into `ld.Config.HTTP`, and the
SDK rebuilds the HTTP config with the new anchor key when it constructs the new client — so the
`Authorization` header is set correctly for the new anchor automatically. The pre-built `SDKHTTPConfig` /
`Client()` (used for event + big-segment transport) is key-independent except for that header, and those
components set their own auth per request rather than reading it from `httpconfig`. No action required.

### H5 — Order of operations (start-new → swap pointer → close-old)

**Answer: The recommended order is necessary but NOT sufficient for the in-memory store.**

Because building the new client is what rebuilds (and empties) the in-memory store (H1), there is a
window after the swap in which evaluations see an empty store until the new anchor finishes its initial
sync — *regardless* of operation order. The PoC shows the env's store is replaced with a fresh,
uninitialized store as soon as the new client is registered.

**Recommended remedy: hand the existing store over to the new client.** Because relay owns the store
implementation (it hands the SDK a single `storeAdapter`), the re-anchor can reuse the existing store for
the new client instead of letting `Build()` construct a fresh one — concretely, make
`SSERelayDataStoreAdapter.Build()` return its existing store when one is already present (or otherwise
seed the new client with the old client's store). The new anchor then reads populated, initialized data
immediately, so there is no empty-store window. Validated by
`TestReanchorPoC_H5_StoreHandoverAvoidsEmptyWindow`.

This is simpler than the alternatives originally considered — gating the swap on `Initialized()`,
mandating a persistent store, or otherwise decoupling store from client — and supersedes them.

**Store-lifecycle caveat:** `streamUpdatesStoreWrapper.Close()` closes the underlying store. With
handover the retiring and new clients share one underlying store, so closing the retiring client must
**not** close it — the adapter (not the client) must own the store's lifecycle. This is not reproducible
with the fake client used in the PoC; verify against the real client in T2.c.

**Recommended order, refined:** start new client (handing over the existing store) → swap anchor pointer
and re-wire peripherals → close old client, ensuring that close does not tear down the shared store.

### H6 — Behavior during the swap window (requests arriving mid-swap)

**Answer: Today there is a gap — `GetClient()` returns nil mid-swap.**

`GetClient()` returns `clients[keyRotator.SDKKey()]`. In the current path the rotator's primary key flips
to the new key **synchronously** inside `UpdateCredential`, but the new client is created on a background
goroutine (`go startSDKClient`) and registered only afterward. The PoC deterministically observes
`GetClient() == nil` in that window (gated client factory, no sleeps). A request arriving mid-swap gets a
nil client.

**T2.c action:** do not advance the anchor pointer until the new client is registered (and ideally
`Initialized()`). Combined with H5/H7, the rule is: **construct + initialize the new client first, then
atomically flip the anchor pointer.**

### H7 — Failure mode: new client init fails

**Answer: Today a failed re-anchor breaks the environment. Atomicity/rollback is required.**

When the new anchor client fails to initialize, the rotator has already flipped the anchor pointer to the
new key, but no client exists for it — so `GetClient()` returns nil **even though the old anchor's client
is still alive and valid** during its grace period. The PoC confirms `GetInitError()` is set, `GetClient()`
is nil, and the old client is still present in the `clients` map (so the data path *could* have been
preserved).

**T2.c action (this is §8's atomicity requirement):** validate that the new client initializes **before**
swapping the anchor pointer; on failure, roll back to the old anchor and preserve the previous accepted
set. Log a structured error and alarm (per §9).

---

## Consolidated requirements for T2.c / T2.d

| # | Requirement | From |
|---|---|---|
| 1 | Construct + initialize the new anchor client **before** flipping the anchor pointer; flip atomically. | H5, H6 |
| 2 | On new-client init failure, roll back to the old anchor; preserve previous accepted set; log + alarm. | H7 |
| 3 | Hand the existing store over to the new client (make `SSERelayDataStoreAdapter.Build` reuse its store) so there is no empty-store window; ensure the retiring client's `Close()` does not tear down the shared store. | H1, H5 |
| 4 | Re-wire big-segment sync on re-anchor (add a replace-credential method, or recreate the synchronizer). | H3 |
| 5 | Continue calling `ReplaceCredential` on the event dispatcher + metrics publisher (already wired in `addCredential`). | §7 table |
| 6 | Expect a duplicate downstream `put` from the new anchor's initial sync; ensure downstream connections are not torn down for retained credentials. | H2 |
| 7 | No `httpconfig` change needed. | H4 |

## What did NOT need changing

- `httpconfig` (H4).
- Downstream SSE routing / `envStreams` (H2) — already credential-scoped and independent of the anchor.
- Event dispatcher + metrics publisher already expose `ReplaceCredential` and are already called from
  `addCredential` on an SDK-key change.

## Test inventory

All tests are in [`internal/relayenv/env_context_reanchor_test.go`](../../internal/relayenv/env_context_reanchor_test.go),
prefixed `TestReanchorPoC_H<n>_…`:

- `H1_SharedStoreAdapterRebuildSemantics` — in-memory rebuild vs. persistent-store preservation.
- `H2_DownstreamConnectionSurvivesReAnchor` — live client-side connection survives the swap.
- `H2_NewClientInitialSyncRebroadcastsPut` — duplicate `put` is produced and counted.
- `H3_BigSegmentSyncIsNotReWiredOnReAnchor` — synchronizer keeps the old key; no re-wire today.
- `H4_HTTPConfigIsKeyIndependentExceptAuthHeader` — only the auth header is key-dependent.
- `H5_InMemoryStoreIsWipedByReAnchor` — store replaced/empty after swap.
- `H5_StoreHandoverAvoidsEmptyWindow` — reusing the store across the swap avoids the empty window (the remedy).
- `H6_AnchorPointerFlipsBeforeNewClientIsRegistered` — `GetClient()` nil mid-swap (deterministic).
- `H7_FailedNewClientLeavesEnvWithoutAnchorClient` — failed swap breaks the env; old client still alive.
