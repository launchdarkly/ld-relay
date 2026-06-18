# Phase 1 — Concurrent SDK Keys in Relay Proxy: Design

**Epic**: [SDK-2453](https://launchdarkly.atlassian.net/browse/SDK-2453)
**Backend tech spec**: [Confluence 4186243250](https://launchdarkly.atlassian.net/wiki/spaces/PD/pages/4186243250/Tech+Spec+Concurrent+SDK+Keys)
**Companion**: [`phase1-plan.md`](./phase1-plan.md) (tasks, sequencing, estimates)

This document is the canonical reference for the *what* and *why* of Phase 1. The companion plan covers *how*. Agents working on individual tasks should read both.

---

## 1. Overview

LaunchDarkly is rolling out **concurrent SDK keys** — the ability for a single environment to have *multiple* SDK keys and *multiple* mobile keys simultaneously. Phase 1 brings this capability to the Relay Proxy.

### Why

Customers (notably Block/Square, Confluent) maintain dozens or hundreds of services that share the same LaunchDarkly environment. Today each service uses the same single SDK key per environment. If that key is compromised, customers face large-scale operational toil rotating it across every service.

Concurrent keys let customers issue distinct keys per service, reducing blast radius and supporting independent key lifecycles.

### Scope

| In scope (Phase 1) | Out of scope |
|---|---|
| Multiple SDK keys per environment | Multiple client-side IDs per environment (deferred to a later project) |
| Multiple mobile keys per environment | Views / payload filtering V2 (Phase 2 — mega stream) |
| Per-key expiry and graceful rotation | Per-key event attribution (analytics events collapse to the env's anchor key) |
| Delivery via **Relay Auto Config (RAC)** | Manual config (TOML / env vars) — stays single-key in Phase 1 (lifted in Phase 2) |
| Delivery via **offline-mode archive** | |
| Implementation in Relay v8, merged forward to Relay v9 | |

### The "trusted source" restriction

Relay authenticates *upstream* with only one SDK key per environment — the **anchor**. Additional keys are accepted locally but not verified upstream. That makes "trust the source of additional keys" load-bearing for safety.

LaunchDarkly-generated sources (RAC, offline archive) are trusted: they only ever carry an environment's real keys, so a wrong-environment key can't appear. Hand-entered manual config is *not* trusted — a typo could silently leak this env's data to an SDK using a wrong-env key and misattribute its events to this env. Phase 1 therefore accepts additional keys *only* from RAC and offline archives.

Manual config support returns in Phase 2 via the mega stream, which verifies every key individually.

Customer impact: ~50% of relay customers use RAC and benefit immediately. The other ~50% (manual config) wait for Phase 2. The team has reviewed this trade-off — see [Confluence 4979425298](https://launchdarkly.atlassian.net/wiki/spaces/PD/pages/4979425298/Relay+Proxy+Auto+Config+Risk+Assessment).

---

## 2. Preamble: How Relay Auto Config (RAC) works

The §2.4 event table is unreadable without this context.

**RAC is a push channel from LaunchDarkly *to* relay.** It's a single long-lived SSE stream over HTTPS. Relay opens it at startup using a special "relay token" (distinct from any SDK key) and consumes the stream's messages.

**Lifecycle**:
1. Relay starts up. If RAC is configured, relay opens an SSE connection to LaunchDarkly's RAC endpoint.
2. LaunchDarkly responds with an initial `put` message containing the full state of every environment this relay should know about. Relay creates an `EnvContext` per env and opens upstream SDK clients on the anchor key.
3. While the SSE stream is open, LaunchDarkly pushes incremental messages: `patch /environments/$ENVID` (state changed), `delete /environments/$ENVID` (removed), `put /` (full refresh — rare, usually on reconnect).
4. Connection drops → relay reconnects with backoff and reconciles against the next `put`.

The path notation (`/environments/$ENVID`) is a **JSON path within the RAC document**, not an HTTP route. It identifies which part of relay's internal state the message addresses. Think JSON Patch semantics.

**Offline mode is the same in shape, different in transport.** No stream. LaunchDarkly tooling generates an archive file with the same `EnvironmentRep` shape. Relay reads it on startup and reconciles on reload.

**Don't confuse RAC with the SDK streaming endpoint.** Relay has two kinds of upstream connection: RAC (one per relay, carries config) and the SDK stream (one per env, anchored by SDK key, carries flag/segment data). Phase 1 changes the SDK-stream-per-env story; RAC itself is unchanged in transport.

## 3. Preamble: RAC vs Manual Config

Relays use *either* manual config *or* RAC — not both. They're alternative top-level configuration approaches.

- **Manual config**: TOML file or `LD_*` env vars list each environment explicitly with its SDK key, mobile key, and env ID. Static — operator edits the file and reloads.
- **RAC**: TOML file has a single `[AutoConfig]` block with a relay token. LaunchDarkly streams the environment list. No `[Environment ...]` blocks needed.

A relay instance is one or the other, decided at deployment time.

RAC is **enterprise-only** in LaunchDarkly's pricing tiers. Lower-tier customers physically cannot use RAC and use manual config by necessity. This pricing reality is why the trusted-source restriction (§1) excludes ~50% of relay customers from Phase 1.

---

## 4. Architectural pillars

Phase 1 rests on three commitments. The whole design is consistent with these; they hold across every code path and test.

### 4.1 One upstream connection per environment, on the anchor

Relay opens exactly one upstream SDK client per env, authenticated with the anchor SDK key. All other accepted keys (server and mobile) are matched locally against the request's `Authorization` header and served off the same data store. They never open their own upstream connection.

This generalizes existing behavior: today's mobile keys and client-side IDs already behave this way (verified in [`internal/relayenv/env_context_impl.go`](../../internal/relayenv/env_context_impl.go); only `config.SDKKey` calls `startSDKClient`). Phase 1 extends "local match only, no upstream client" to non-anchor *server* keys.

**Trade-off**: this is a deliberate choice over the alternative (one upstream client per accepted key, the approach in Matthew Keeler's PoC at PR #675). Reasons we chose single-anchor:
- **Connection-count efficiency** — a customer with 50 keys × 10 envs × 10 filters would otherwise hold thousands of upstream streams.
- **Phase 2 alignment** — Phase 2's mega stream is one connection per environment. Single-anchor is closer to that target.

The cost is re-anchoring complexity (§7). We accept that cost.

### 4.2 The anchor

The anchor is **the SDK key the singular `sdkKey.value` field points to**, identified by byte-equality against an entry in the `sdkKeys[]` array. There is no `isDefault` flag in the wire format — the value match *is* the signal.

The backend designates the anchor; relay is passive. Relay reads `sdkKey.value` and uses that key for upstream.

**Invariants** (maintained by the backend):
- `sdkKey` always names a non-expiring key.
- The backend blocks deleting or expiring the last non-expiring key in an environment. On default rotation, the backend promotes another non-expiring key first, then flips `sdkKey.value`.
- The new anchor's entry in `sdkKeys[]` continues to carry no `expiry`. The old anchor (now demoted) carries an `expiry`.

**Re-anchor trigger**: whenever `sdkKey.value` changes. This is the single trigger for an upstream-client swap. See §7 for the mechanism.

**Mobile-key analog**: the singular `mobKey` field is the default mobile key for events. No upstream connection — mobile keys are local-match-only — but `mobKey` plays the same back-compat singular-pointer role.

### 4.3 Events collapse to anchor per kind

Analytics events forward upstream under the env's anchor key of each kind. Two dispatchers per env: one for SDK events under `sdkKey.value`, one for mobile events under `mobKey`. The dispatcher uses its stored `authKey`, not the credential on the incoming request.

**Why no per-key event attribution**: SDK keys are *secrets* and not appropriate as metric/analytics tags. LaunchDarkly provides customer-facing tagging mechanisms (context attributes, environment tags) for slicing events. The trusted-source restriction makes anchor attribution safe — every accepted key truly belongs to this env, so anchor attribution lands on the right env. We lose per-key granularity, not env correctness.

**Asymmetry — diagnostic events**: diagnostic events (SDK self-reported initialization, errors) take a different code path. They proxy the incoming request's headers verbatim, including the Authorization header carrying the original credential. This preserves operational debug value — *which* SDK reported this — at the cost of asymmetry with analytics. We accept this. (Long-term, a metadata-header approach could provide symmetric attribution; out of scope for Phase 1.)

---

## 5. Wire format

Both RAC and the offline archive carry the same `EnvironmentRep`. One parsing change covers both sources. Producers already emit this format — relay can implement and test against captured payloads today.

### Example RAC `event:put`

```json
{
  "path": "/",
  "data": {
    "environments": {
      "68e5179e8307e4099c277e2a": {
        "envId": "68e5179e8307e4099c277e2a",
        "envKey": "production",
        "envName": "Production",
        "projKey": "...",
        "projName": "...",
        "secureMode": false,
        "version": 26,
        "sdkKey": { "value": "sdk-9409..." },
        "mobKey": "mob-f41c...",
        "sdkKeys": [
          { "key": "new-production-default", "value": "sdk-9409..." },
          { "key": "another-one",            "value": "sdk-38b0..." }
        ],
        "mobileKeys": [
          { "key": "mob-key-50bca22351", "value": "mob-f41c..." }
        ]
      }
    }
  }
}
```

The offline archive wraps the same `env` object per entry: `{"env": ..., "dataId": "..."}`.

### Shape rules

- Array entries: `{ "key": <identifier>, "value": <credential secret>, "expiry"?: <Unix-ms> }`.
- Singular `sdkKey` is an **object** (`{"value": ...}`); may carry the legacy `expiring{value, timestamp}` slot during default rotation.
- Singular `mobKey` is a **plain string**. Shape asymmetry is historical (mobile keys never had a legacy expiring slot).
- Anchor = `sdkKeys[]` entry whose `value` matches `sdkKey.value`. No `isDefault` flag — value match is the signal.
- Arrays are *inclusive* of the default — the anchor entry is *in* `sdkKeys[]`, not separate.
- `expiry` is present only while a key is expiring; omitted otherwise; never null.
- The legacy `sdkKey.expiring{}` slot is populated **only during default rotation** (old-relay back-compat). Non-default key expiring uses only the array `expiry`.
- Old relays ignore unknown JSON fields and continue using singular `sdkKey`/`mobKey` — additive, fully backward-compatible.

### Terminology

Aligned with the backend tech spec's `accounts.sdk_keys` table:

- **`name`** = display name (e.g. "Default SDK Key"). Used in the UI. **Not in the wire format** — relay doesn't need it.
- **`key`** = identifier (e.g. "default-sdk"). Non-secret. Carried in wire as `key`.
- **`value`** = the credential secret (e.g. `sdk-xxxx-...`). Carried in wire as `value`.

**Naming trap in code**: relay's existing types `SDKKey`, `MobileKey`, `SDKCredential` refer to what the wire format calls `value`. The wire's `key` field is the *identifier*, a different thing. Do not rename the existing relay types — they're stable — but call out the trap in code comments.

A canonical comment for the wire-type definition (subject to bikeshed at PR time):

```go
// EnvironmentRep carries an environment's wire shape from RAC and the offline
// archive (same struct serves both — keep them aligned).
//
// FIELD NAMING — read this before changing anything:
//
//   sdkKey  is the singular *default* SDK key for the environment. It's an
//           object ({"value": "sdk-..."}) so it can also carry the legacy
//           sdkKey.expiring{value, timestamp} slot during default rotation
//           (back-compat for relays predating concurrent keys).
//
//   mobKey  is the singular default mobile key. It's a *plain string*
//           because mobile keys never had a legacy expiring slot. The shape
//           asymmetry is historical, not a design choice.
//
//   sdkKeys/mobileKeys  are the authoritative full accepted set. Entries:
//                       { key: <identifier>, value: <credential>, expiry?: <ms> }
//
// TERMINOLOGY:
//   The wire "key" field is the human-readable IDENTIFIER (e.g. "default-sdk"),
//   non-secret. The wire "value" field is the actual CREDENTIAL string (e.g.
//   "sdk-xxxx-..."), which is the secret. Note that relay's own types
//   (SDKKey, MobileKey, SDKCredential) refer to what the wire calls "value" —
//   they're misnamed by today's standards but stable, so do not rename.
//
// Anchor selection: anchor = the sdkKeys entry whose `value` matches
// sdkKey.value. No isDefault flag. See phase1-design.md §4.2.
```

---

## 6. Credential lifecycle

### 6.1 Expiry model

Each entry in `sdkKeys[]` / `mobileKeys[]` carries an optional `expiry` field (Unix-ms timestamp). When present, the key is being phased out — relay drops it when `expiry` passes. When absent, the key is permanent.

**Two removal paths**:

- **Graceful**: key has `expiry` set. Relay's existing periodic ticker (`StepTime` → `cleanupExpiredCredentials`) drops the key when the timestamp passes and disconnects downstream SDKs using it.
- **Immediate**: key omitted from the next RAC patch / archive reload. Relay diffs the accepted set on reconcile, finds the missing key, and revokes it now.

**Edge case**: a key was in graceful state, then omitted entirely → treat as immediate (race-ahead-of-timer).

### 6.2 Generalize the `Rotator`

Today's `Rotator` ([`internal/credential/rotator.go`](../../internal/credential/rotator.go)) tracks one primary SDK key + one deprecated-with-expiry slot + single primary mobile key + single primary env ID. Generalize to: a *set* of accepted keys (server + mobile) with optional per-key expiry, plus a designated anchor.

**Reuse the existing `StepTime` machinery** — generalize from the single `expiring` slot to per-array-key. No new periodic infrastructure.

**Mobile-key panic**: today, `Rotator.RotateWithGrace(MobileKey, gracePeriod)` panics with `"programmer error: mobile keys do not support deprecation"`. The panic is a guard against an unsupported API state, not a safeguard against a hazard — there was no data-model slot for an expiring mobile key, so the code failed loud rather than store junk. Phase 1's data-model generalization provides the slot; the panic guard is removed alongside.

### 6.3 Legacy `sdkKey.expiring{}` back-compat

On default rotation the backend mirrors expiry info into both:
- The old default's entry in `sdkKeys[]` gets `expiry: <ts>` (new field).
- The legacy `sdkKey.expiring{value, timestamp}` slot gets the same (old field, for old relays).

**Decision**: new relays trust the array. The legacy `sdkKey.expiring{}` field is treated as a write-only back-compat shim — new relays do not read it. (Working assumption pending team confirmation.)

---

## 7. Re-anchoring

When `sdkKey.value` changes (voluntary rotation *or* current default expiring and being replaced by a promoted non-expiring key), relay must swap its upstream client to the new anchor while preserving downstream SDK connections.

This is the highest-risk piece of Phase 1. The **T0 PoC** validated the swap mechanism against seven hypotheses; the durable tests live in `internal/relayenv/env_context_reanchor_test.go` and the per-hypothesis findings are in [`phase1-T0-reanchor-poc-findings.md`](./phase1-T0-reanchor-poc-findings.md). The findings are summarized below as the spec for T2.c / T2.d.

### Required order of operations

```
1. Build the new anchor's SDK client, handing over the existing data store (do not flip the anchor pointer yet).
2. Wait for the new client to report Initialized() == true.
3. Atomically flip the rotator's anchor pointer.
4. Call ReplaceCredential on event dispatcher + metrics publisher.
5. Re-wire (or recreate) big-segment sync.
6. Close the old anchor's client (after its grace period elapses for downstream traffic), ensuring its Close() does not tear down the now-shared store.
```

This order is *necessary* — the PoC found that flipping the pointer too early leaves `GetClient()` nil mid-swap (H6) and breaks the env on init failure (H7) — and *sufficient* only with the store-handling approach below.

### The data store: hand the existing store over to the new client

An earlier version of this design assumed two SDK clients pointed at the same env would feed the *same* data store as a side-effect. The PoC (H1, H5) showed this is **wrong for the in-memory store**: each SDK client construction calls `storeAdapter.Build()`, which atomically swaps in a *new, empty* store, so the new client would otherwise have to re-sync from scratch (an empty-store window). This affects only the in-memory case; with a persistent store (Redis, DynamoDB) the data lives outside the wrapper and survives the swap.

**Chosen remedy: hand the existing store over to the new client.** Because relay owns the store implementation (it hands the SDK a single `storeAdapter`), the re-anchor reuses the existing store for the new client instead of letting `Build()` construct a fresh one — concretely, make `SSERelayDataStoreAdapter.Build()` return its existing store when one is already present (or otherwise seed the new client with the old client's store). The new anchor then serves populated, initialized data immediately, with no empty-store window. Validated by `TestReanchorPoC_H5_StoreHandoverAvoidsEmptyWindow`.

This is the concrete form of decoupling the store's lifecycle from the client's. Two alternatives were considered and rejected as heavier: gating the swap on the new client reaching `Initialized()` (still leaves a window for already-connected reads and keeps the store coupled to client construction), and mandating a persistent store for graceful re-anchor (constrains deployments).

**Store-lifecycle caveat:** `streamUpdatesStoreWrapper.Close()` closes the underlying store. With handover the retiring and new clients share one underlying store, so closing the retiring client must **not** close it — the adapter (not the client) must own the store's lifecycle. (Not reproducible with the fake client used in the PoC; verify against the real client in T2.c.)

### Component re-wiring on re-anchor

| Component | Today | On re-anchor |
|---|---|---|
| Data store | Rebuilt per client by `storeAdapter.Build()` | Hand the existing store over (adapter reuses it); the retiring client's `Close()` must not tear it down (T2.c) |
| Event dispatcher | Has `ReplaceCredential` | Call `ReplaceCredential` (T2.c) |
| Metrics publisher | Has `ReplaceCredential` | Call `ReplaceCredential` (T2.c) |
| Big-segment sync | Wired at construction; no replacement method | Re-wire (add a replace-credential method) **or** recreate the synchronizer (T2.d). Recreate is simpler; re-wire preserves in-flight sync state. |
| `httpconfig` | Built via injected *builder* (not pre-built config) | **No change needed** — the SDK rebuilds with the new anchor key automatically (PoC H4) |
| Downstream SSE connections | Keyed on `ScopedCredential`, independent of anchor | Survive automatically (PoC H2); expect one duplicate `put` from the new client's initial sync |

### Failure handling

If the new client fails to initialize, the swap **rolls back**: the rotator's anchor pointer stays on the old key, the previous accepted set is preserved, a structured error is logged, and an alarm is raised. The old anchor's client (still alive in its grace period) continues to serve. This is the §8 atomicity principle applied to re-anchor.

### Consolidated specification for T2.c / T2.d

| # | Requirement | Source | Owner |
|---|---|---|---|
| 1 | Build + initialize the new anchor client *before* flipping the pointer; flip atomically. | H5, H6 | T2.c |
| 2 | On init failure, roll back to old anchor; preserve previous accepted set; log + alarm. | H7 | T2.c |
| 3 | Hand the existing store over to the new client (adapter reuses its store); ensure the retiring client's `Close()` does not tear down the shared store. | H1, H5 | T2.c |
| 4 | Re-wire big-segment sync on re-anchor (recreate or replace-credential). | H3 | T2.d |
| 5 | Call `ReplaceCredential` on event dispatcher + metrics publisher. | §7 | T2.c (already wired in `addCredential`) |
| 6 | Expect duplicate downstream `put`; retain connections for credentials still in the accepted set. | H2 | T2.c (awareness) |
| 7 | No `httpconfig` change. | H4 | n/a |

---

## 8. Processing & lifecycle

Three event types, two source paths (RAC + offline), one integration point (`ReconcileCredentials` on `EnvContext`).

| Event | RAC trigger | Offline trigger | Relay action |
|---|---|---|---|
| Env added | `patch /environments/$ENVID` (env not known) | new archive entry on reload | Create env; open upstream connection on anchor (online only); map all accepted keys into lookup |
| Keys change | `patch /environments/$ENVID` (env known, payload differs) | archive update on reload | Reconcile accepted `sdkKeys`/`mobileKeys`; re-anchor if `sdkKey.value` changed |
| Env deleted | `delete /environments/$ENVID` | env removed from reloaded archive | Tear down env + upstream connection + mappings |

### Order of operations (keys change)

Within a single `keys change` event, the order is:

1. **Add new keys** (new credential entries added to accepted set, handlers built).
2. **Re-anchor** if `sdkKey.value` changed (swap upstream client, re-wire peripheral components).
3. **Remove expiring/omitted keys** (drop entries, disconnect downstream SDKs that were using them).

This order ensures the accepted set is a *superset* during the transition. The new anchor's client comes up before the old anchor's client tears down. Downstream SDKs are never spuriously rejected mid-update.

### Atomicity

Reconcile is **all-or-nothing**. On partial failure (malformed payload, new-client init failure, etc.), log a structured error and preserve the previous accepted set. Working assumption — open question for the team. Aligns with the malformed-payload policy (§9).

### Edge cases

- **De-expiry** (key was expiring; new payload omits `expiry`): cancel the scheduled drop.
- **Rename** (same `value`, different `key` identifier): no-op for credential set; only update status-endpoint display.
- **Mixed update** (add + re-anchor + remove in one patch): apply in the order above.

---

## 9. Defensive behavior — malformed payloads

When relay receives a malformed RAC payload — most importantly, `sdkKey.value` not present in `sdkKeys[]`, or `sdkKey` field missing entirely — the backend invariants of §4.2 have been violated.

**Decision** (confirmed with the team):

1. **Preserve the previous accepted set.** Do not apply the malformed update. Log a structured error. Alarm.
2. **Disconnect and reconnect the RAC stream with jitter.** The backend believes the patch was applied — RAC is one-way push and relay has no NAK channel. Without a reconnect the backend won't send a fresh state; it expects relay to be in sync. Reconnecting forces a fresh `put` on the new connection, which gives relay a clean baseline.
3. Do *not* silently fall back to the first entry in `sdkKeys[]` (silent and dangerous).
4. Do *not* leave the env in a half-applied state.

This is the same atomicity principle as §8, applied at the boundary between trusted-source input and relay's internal state, with the added piece (reconnect) needed because RAC has no acknowledgment mechanism for failed-payload rejection.

---

## 10. Backwards compatibility

Two assertions:

1. **Payload is additive.** Relays that don't understand the new `sdkKeys`/`mobileKeys` array fields ignore them and continue using the singular `sdkKey`/`mobKey`. No coordinated upgrade required.
2. **One representation for all relays and both sources.** No per-version fork of `EnvironmentRep`.

**Bidirectional upgrade compat**: customers can upgrade their backend before relay, or relay before backend, in any order. The slowest party uses singular fields; the faster party emits arrays. Both states converge to single-key behavior until both are upgraded.

**Verification**: `DisallowUnknownFields` is not used anywhere in relay's env-parse path. The additive claim holds — Go's default JSON decoder silently ignores unknown fields. Confirmed as T3.a pre-work.

**Downgrade story**: open question for the team. Rolling relay back from Phase 1 to a pre-Phase-1 build means SDKs using non-anchor keys would lose connectivity. Document as a release-note consideration.

---

## 11. Manual configuration

Manual config (TOML file or `LD_*` env vars) continues to support **exactly one SDK key + one mobile key + one env ID per environment**, as today. The schema doesn't change. Manual-config customers see zero behavior change from Phase 1.

**The PoC's manual-multi-key additions must not be inherited.** SDK-2415 added `AdditionalSDKKeys` and `LD_ADDITIONAL_SDK_KEYS_*` support. We deliberately *do not* want this. T3 review must verify neither pattern appears in `config/config.go` or `config/config_validation.go`.

---

## 12. Internal model

```
Environment
  envID
  identifiers (key, name, proj…)
  anchorKey            (the one upstream-auth key)
  acceptedKeys: KeySet (server + mobile, local match)
  clientSideID         (single)
  upstreamConnection   (one, on the anchor)
  dataStore
       │
       └─── KeySet
              keys (equivalent peers, server or mobile)
              per-key optional expiry
```

**`KeySet`** generalizes today's `Rotator` ("primary + deprecated-with-expiry") into "set of accepted keys + anchor."

**Routing/auth is a local lookup**: a connecting credential is matched against the accepted set → the environment → served off the single anchor connection. The env ID registers exactly once.

**No per-view structure anywhere in Phase 1.** Premature abstraction — Phase 2's mega stream is still speculative. Keep the model flat. A key is just an accepted credential. Don't preemptively add `viewKeys` fields or "scope" abstractions.

---

## 13. Recorded decisions

| Decision | Rationale | Alternatives rejected |
|---|---|---|
| Trusted sources only (RAC + offline archive) in Phase 1 | Relay can't verify additional keys upstream; trusted sources guarantee correct env→key mapping | Manual multi-key with verification (no suitable verification endpoint; staleness problem; Phase 2 resolves anyway), opt-in unsafe flag (same concerns) |
| Single upstream connection per env on the anchor | Connection-count efficiency at scale; aligns with Phase 2's single-mega-stream model | Multi-client (SDK-2415 PoC approach): trades re-anchor complexity for fan-out at customer scale |
| Anchor by `sdkKey.value` byte-match (no `isDefault` flag) | Single source of truth; matches what RAC already emits | `isDefault` flag (would require backend wire change and dual sources of truth) |
| Per-key `expiry` (Unix-ms) on array entries | Confirmed real format from producers; reuses existing ticker | Per-env single deprecated slot (today's model — doesn't scale to multi-key) |
| Trust the array on expiry disagreement | Simpler invariant; legacy field becomes write-only shim | Take whichever is later, hard-fail on disagreement (more complex, no clear value) |
| Events collapse to anchor per kind, no per-key attribution | Keys are secrets — not appropriate as analytics tags; LD provides better tagging mechanisms | Per-key attribution (would multiply event machinery N×) |
| Diagnostic events keep verbatim-proxy behavior | Preserves operational debug value (which SDK reported); minimal code change | Collapse diagnostic to anchor (loses debug signal); metadata-header (long-term direction, out of Phase 1 scope) |
| `ReconcileCredentials` API replaces `UpdateCredential` everywhere | Atomic semantics; single API surface; no external consumers to preserve | Keep both methods (two ways to do the same thing); stateful batching (non-idiomatic Go) |
| On malformed payload: preserve previous state **+ reconnect RAC stream with jitter** | Loud, safe, atomic — and forces backend to push a fresh `put`, since RAC has no NAK | Soft-fall-back to `sdkKeys[0]` (silent, order-dependent); refuse to serve until next valid update (disruptive); preserve-without-reconnect (backend stays out of sync until something else triggers a refresh) |
| Order of operations: add → re-anchor → remove | Accepted set is a superset during transition; downstream survives | Remove first (downstream-availability window); concurrent (race-prone); atomic batch (atomicity breaks at goroutine boundary) |
| Re-anchor: keep old store/anchor authoritative until new client `Initialized()` | The in-memory store is rebuilt empty on new-client construction (PoC H1, H5); must keep old serving until new is ready | Require persistent store (limits feature to a subset); decouple store from client lifecycle (much bigger refactor) |
| Re-anchor: validate new client `Initialized()` before flipping the anchor pointer; rollback on failure | Avoids mid-swap nil `GetClient()` (PoC H6) and stranded-anchor on init failure (PoC H7) | Flip-then-init (today's broken behavior); accept the gap (visible to customers) |
| Manual config stays single-key in Phase 1 | Same trusted-source reasoning as above | Verify-on-startup, opt-in unsafe flag (rejected for the same reasons in §1) |

---

## 14. Resolved questions

All design-blocking questions have been answered.

- **Q5** (RAC propagation SLA for `sdkKey.value` changes): **Real-time.** Same delivery semantics as flag eval / delivery in the SDK.
- **Q6** (Behavior on malformed RAC payload): **Preserve previous accepted set + reconnect the RAC stream with jitter** to force a fresh `put` from the backend (the backend believes the patch was applied because RAC has no NAK channel). See §9.
- **Q7** (Legacy `sdkKey.expiring{}` vs per-key `expiry` disagreement policy): **Trust the array.** Legacy field is a write-only back-compat shim; new relays ignore it on read.
- **Q8** (Per-credential downstream tracking for targeted disconnect): **Already implemented** — today's rotation/disconnect path uses it. T1.c builds on the existing tracking; does not have to construct new infrastructure.
- **Q11** (Customer downgrade story): **No mitigation work.** Documented in release notes; customers reverting from Phase 1 understand they lose multi-key support.

T0 PoC findings (re-anchoring mechanics) are recorded in §7 and in [`phase1-T0-reanchor-poc-findings.md`](./phase1-T0-reanchor-poc-findings.md).

---

## 15. Glossary

- **Anchor**: the SDK key the singular `sdkKey.value` field designates. Used for the upstream connection and as the event-dispatcher's stored `authKey`.
- **Accepted set**: all SDK keys + mobile keys + env IDs an environment will accept for downstream-SDK authentication. Includes the anchor.
- **Identifier** (wire `key`): the non-secret human-readable name of a credential. Used in API paths and status display.
- **Credential / value** (wire `value`): the actual secret string (e.g. `sdk-xxxx-...`). What relay's existing types `SDKKey`/`MobileKey`/`SDKCredential` refer to.
- **RAC** (Relay Auto Config): the push channel by which LaunchDarkly delivers environment configuration to enterprise relays. SSE over HTTPS.
- **Offline archive**: a file generated by LaunchDarkly tooling carrying the same `EnvironmentRep` shape. Reloaded periodically.
- **Re-anchor**: swap the upstream SDK client when `sdkKey.value` changes. Single trigger for the swap mechanism.
- **Trusted source**: a LaunchDarkly-generated configuration source (RAC or offline archive). Guaranteed to carry only the environment's real keys.
