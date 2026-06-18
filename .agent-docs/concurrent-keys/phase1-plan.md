# Phase 1 — Concurrent SDK Keys in Relay Proxy: Plan

**Epic**: [SDK-2453](https://launchdarkly.atlassian.net/browse/SDK-2453)
**Companion**: [`phase1-design.md`](./phase1-design.md) (architecture, decisions, model)

This document covers *how* we ship Phase 1: branching, sequencing, tasks, dependencies, estimates, test strategy, and rollout.

---

## 1. Branching strategy

**Long-lived feature branch off v8: `feat/concurrent-keys`.** Team convention for feature branches across LD repos — recognizable to other team members.

- All sub-task PRs target `feat/concurrent-keys`, not v8 directly.
- Sub-PR branches follow Aaron's convention: `aaronz/<sub-task-ticket-id>/<task-slug>`, where `<sub-task-ticket-id>` is the **specific sub-task ticket** (e.g. `SDK-2521` for T1.0), **not the epic SDK-2453**. Example: `aaronz/SDK-2521/T1.0-rotate-with-grace-mobile-fix`.
- **Regularly merge `v8` into `feat/concurrent-keys`** (weekly) to surface conflicts incrementally rather than at the end.
- **Final merge** `feat/concurrent-keys` → v8 happens when the feature is fully ready and verified, as a single feat commit.

### Worktree

```bash
git worktree add ../ld-relay-wt-feat-concurrent-keys -b feat/concurrent-keys v8
```

This document is committed in that worktree at `.agent-docs/concurrent-keys/phase1-plan.md`.

---

## 2. Wave breakdown

Three waves. Wave 3 is release-time work: clean up project scaffolding, merge `feat/concurrent-keys` to v8 and release, then merge-forward to v9 when v9 is ready (possibly weeks or months later).

| Wave | Theme | When |
|---|---|---|
| **Wave 1** | Foundations: PoC, data structures, wire types, test infrastructure | Immediately, multiple sub-tasks in parallel |
| **Wave 2** | Core implementation: API surface change, re-anchor mechanism, peripheral re-wiring, end-to-end integration | After PoC findings + Wave 1 data structures land |
| **Wave 3** | Release: code cleanup → merge to v8 → publish release → merge-forward to v9 | Cleanup + v8 release happen as soon as Wave 2 completes; merge-forward to v9 is calendar-deferred |

The "single-key behavior unchanged at every PR boundary" invariant is the load-bearing testable property. Every sub-PR must preserve it.

---

## 3. Task list

Each task has: ticket name, files touched, dependencies, estimates. Acceptance criteria live in the JIRA ticket; rationale lives in [`phase1-design.md`](./phase1-design.md).

### Wave 1

| Task | Files | Depends on | Human | AI agent |
|---|---|---|---|---|
| **T0** — Re-anchoring PoC | `internal/relayenv/` (new test files) | — | 3-5 days | 1-2 days (with iteration) |
| **T1.0** — Remove `RotateWithGrace` mobile-key panic | `internal/credential/rotator.go` | — | 0.5 day | 30 min - 1 hr |
| **T1.a** — Add Rotator accepted-set data structures | `internal/credential/rotator.go`, `credential.go` | T1.0 | 1-2 days | 1-2 hrs |
| **T3.a** — Extend `EnvironmentRep` + verify | `internal/envfactory/*`, possibly `archive_reader.go` | — | 1-2 days | 1-2 hrs |
| **T5.a** — Integration test harness | New test infrastructure dir | — | 2-3 days | 2-3 hrs |
| **T5.b** — Events payload regression test + baseline | `internal/events/*_test.go` (new) | — | 1-2 days | 1-2 hrs |

**Wave 1 total**: 8.5-14.5 human days; 6-10 AI agent hours.

### Wave 2

| Task | Files | Depends on | Human | AI agent |
|---|---|---|---|---|
| **T1.b** — `ReconcileCredentials` API + migrate call sites + remove `UpdateCredential` | `internal/relayenv/env_context*.go`, both action handlers, tests | T0, T1.a | 2-3 days | 2-4 hrs |
| **T1.c** — Generalize cleanup ticker for per-key expiry + mobile-key disconnects | `internal/credential/rotator.go`, `env_context_impl.go` | T1.b, Q8 verified | 2-3 days | 2-3 hrs |
| **T2.a** — `addCredential` anchor-only client | `internal/relayenv/env_context_impl.go` | T0, T1.b | 1-2 days | 1-2 hrs |
| **T2.b** — `GetClient()` returns anchor | `internal/relayenv/env_context_impl.go` | T2.a | 0.5 day | 30 min - 1 hr |
| **T2.c** — Re-anchor mechanism per PoC findings | `internal/relayenv/env_context_impl.go` | T0, T2.a | 3-5 days | 3-5 hrs |
| **T2.d** — Big-segment sync + `httpconfig` from anchor | `internal/relayenv/env_context_impl.go`, big-segment code | T0 | 2-3 days | 2-3 hrs |
| **T2.e** — Handler fan-out optimization | `internal/relayenv/env_context_impl.go`, stream provider interface | T2.c | 2-3 days | 2-3 hrs |
| **T3.b** — Shared reconcile helper | New helper in `internal/envfactory/` | T1.b | 2-3 days | 2-3 hrs |
| **T3.c** — Wire RAC + offline handlers | `relay/autoconfig_actions.go`, `relay/filedata_actions.go` | T3.a, T3.b, T1.b | 2-3 days | 2-3 hrs |
| **T4** — Status endpoint arrays | `internal/api/status_reps.go`, `relay/endpoints_status.go` | T1.b, T3.c | 1-2 days | 1-2 hrs |

**Wave 2 total**: 18-30 human days; 18-32 AI agent hours.

### Wave 3

| Task | Files | Depends on | Human | AI agent |
|---|---|---|---|---|
| **T5.f** — Code cleanup: remove project scaffolding before v8 merge | `.agent-docs/concurrent-keys/` (entire directory), `internal/relayenv/env_context_reanchor_test.go`, anything else project-specific | All Wave 2 terminal sub-tasks (T1.c, T2.b, T2.d, T2.e, T4) | 0.5-1 day | 30 min - 1 hr |
| **T5.g** — Merge `feat/concurrent-keys` to v8 + publish release | None (release activity) | T5.f | 0.5-1 day | n/a (release task, not coding) |
| **T5.e** — Merge-forward to v9 | `internal/relayenv/*`, streaming path | T5.g (and calendar — may be weeks/months after the v8 release) | 3-7 days | 1-2 days (with iteration) |

**Total project**: ~6-11 weeks of full-time human work for code work; the merge-forward to v9 (T5.e) lives on its own calendar that depends on v9 readiness.

---

## 4. Dependency graph

```
                                                  ┌─→ T1.b ─┬─→ T1.c
                                                  │         ├─→ T2.a ─→ T2.b
                                                  │         │      └──→ T2.c ─→ T2.e
                                                  │         ├─→ T3.b ─→ T3.c
                                                  │         └─→ T4
T0 (PoC) ──────────────────────────────────────── ┤
                                                  ├─→ T2.a (PoC needed for swap mechanism)
                                                  ├─→ T2.c
                                                  └─→ T2.d

T1.0 ─→ T1.a ─────────────────────────────────────┘

T3.a ─────────────────────────────────────────────┐
                                                   ├─→ T3.c
T3.b ─────────────────────────────────────────────┘

T5.a (test harness) — supports all other tasks' tests
T5.b (events regression) — runs continuously after landing

[Wave 2 terminal nodes: T1.c, T2.b, T2.d, T2.e, T4] ─→ T5.f (cleanup) ─→ T5.g (merge to v8 + release) ─→ T5.e (merge-forward to v9)
```

**Critical path** (longest dependency chain):
T1.0 → T1.a → T1.b → T2.a → T2.c → T2.e → T5.f → T5.g → T5.e

The Wave 2 portion is roughly: 0.5 + 1.5 + 2.5 + 1.5 + 4 + 2.5 = ~12.5 human days at the midpoint. Wave 3 adds ~1 day (cleanup) + ~1 day (merge/release) + 3-7 days (v9 merge-forward, calendar-deferred). Other Wave 2 tasks parallelize off this critical path.

---

## 5. Implementation notes per task

For each task, JIRA tickets carry the full acceptance criteria. Below are *notes that don't fit cleanly into a ticket* — design rationale, code references, things to watch for.

### T0 — Re-anchoring PoC

The PoC validates the swap mechanism that T2.c will implement. It is *the* prerequisite — without it, T2 is speculation. The PoC's deliverable is durable test code that survives into T2.

Seven hypotheses to validate (each becomes a test):
1. Two clients sharing a `storeAdapter` don't corrupt store invariants.
2. Downstream SSE connections tolerate the swap.
3. Big-segment sync keeps working after re-anchor (or identify what re-wiring is needed).
4. `httpconfig` stays functional after re-anchor.
5. Settle order of operations (start-new → swap pointer → close-old vs. alternatives).
6. Behavior during the swap window.
7. Failure modes: new client init fails.

### T1.0 — Remove `RotateWithGrace` mobile-key panic

Today: `rotator.go:168-169` panics with `"programmer error: mobile keys do not support deprecation"`. The panic is a guard against an unsupported API state (the data model has no slot for an expiring mobile key). Removing the panic is small; the slot is added by T1.a.

### T1.a — Rotator data structures

Internal fields only. No API change. Existing public methods (`SDKKey()`, `GetCredentials()`, etc.) continue to return what they return today by reading from the new internal state where the single primary maps to a one-element set.

Reviewer-friendly comment to add at the top of the new fields: `// Consumed by T1.b (ReconcileCredentials API). See .agent-docs/concurrent-keys/phase1-design.md §6.2.`

### T1.b — `ReconcileCredentials` API

The new method replaces `UpdateCredential` *everywhere* — both call sites migrate in this same PR, and `UpdateCredential` + supporting types are removed. There are no external consumers to preserve.

Today's API surface (to be removed):

```go
// internal/relayenv/env_context.go:80-85
UpdateCredential(update *CredentialUpdate)

// internal/relayenv/env_context.go:27-36
type CredentialUpdate struct {
    primary credential.SDKCredential
    deprecated config.SDKKey
    expiry time.Time
    now time.Time
}
```

The new API (bikeshed the exact signature at PR time; this is illustrative):

```go
ReconcileCredentials(newSet AcceptedSet, anchor credential.SDKCredential) error
```

`AcceptedSet` carries the full new state (server keys + mobile keys with optional per-key expiry). The implementation owns the order of operations (`add → re-anchor → remove`) internally; callers don't sequence.

**On malformed payload**: `ReconcileCredentials` should signal the malformed condition (return a structured error) so that the caller can both (a) preserve the previous accepted set and (b) trigger a reconnect of the RAC stream with jitter (per design §9). T1.b owns the API contract; T3.b/c own driving the reconnect.

### T1.c — Cleanup ticker

Generalize `cleanupExpiredCredentials` (called from `StepTime`) to walk the entire accepted set per kind and drop entries whose `expiry` has passed. The downstream-disconnect logic must handle mobile-key disconnects, not just SDK-key ones.

**Q8 confirmed by team**: per-credential downstream tracking is *already implemented* — today's rotation/disconnect path uses it. T1.c builds on the existing tracking. No new infrastructure to construct; scope is *narrower* than originally feared.

### T2.a — `addCredential` anchor-only client

The switch case at `env_context_impl.go:448-463` currently calls `startSDKClient` for any `config.SDKKey`. Phase 1 narrows this to "only the anchor calls `startSDKClient`." Non-anchor server keys get handlers + `envStreams` + lookup mapping but no upstream client. Mobile keys and env IDs already behave this way.

### T2.b — `GetClient()` returns anchor

`GetClient()` at `env_context_impl.go:580-594` already returns `c.clients[c.keyRotator.SDKKey()]`. With anchor-only client construction (T2.a), this becomes "return the only client." Verify behavior in tests; small change.

### T2.c — Re-anchor mechanism

The big one. PoC findings (design §7 + [`phase1-T0-reanchor-poc-findings.md`](./phase1-T0-reanchor-poc-findings.md)) turned this from "TBD per PoC" into a concrete specification:

1. **Build** the new anchor's SDK client (do *not* flip the anchor pointer yet).
2. **Wait** for the new client to report `Initialized() == true`.
3. **Atomically flip** the rotator's anchor pointer. Until this moment, `GetClient()` returns the **old** client and evaluations are served from the old store.
4. **Call `ReplaceCredential`** on the event dispatcher and metrics publisher.
5. **Re-wire** (or recreate) big-segment sync — T2.d owns this piece; T2.c calls into it.
6. **Close** the old upstream client after its grace period elapses for any retained downstream traffic.

**On new-client init failure**: roll back. Anchor pointer stays on the old key, previous accepted set is preserved, structured error logged, alarm raised. The old client (still alive in its grace period) continues to serve.

**Why this order matters** (PoC H1, H5, H6, H7):
- Flipping the pointer before the new client is registered leaves `GetClient()` returning nil mid-swap (H6).
- Flipping the pointer on init failure strands the env with no usable client even though the old one is fine (H7).
- The in-memory store is rebuilt empty when the new client constructs (H1, H5) — keeping the old anchor authoritative until `Initialized()` is the only way to avoid an evaluation gap without requiring a persistent store.

**Awareness for T2.c**: downstream SSE connections survive the swap automatically but will receive *one duplicate `put`* from the new client's initial sync (PoC H2). This is tolerable and expected — don't treat it as a bug.

### T2.d — Big-segment sync re-wire on re-anchor

T2.d's single responsibility (after PoC): re-wire big-segment sync when the anchor changes. Choose one approach at PR time:

- **Recreate**: destroy and reconstruct the `BigSegmentSynchronizer` on each re-anchor. Simpler; loses any in-flight sync state.
- **Replace-credential**: add a method to the synchronizer interface that updates its SDK key in place. Preserves in-flight state; requires a new method on the interface.

**Recommendation**: recreate, unless we discover in-flight state preservation matters for a specific big-segment customer scenario. Recreate is the easier path; switch to replace-credential only if needed.

`httpconfig` was previously scoped to this task — **PoC H4 confirmed no `httpconfig` change is needed**. The SDK rebuilds the HTTP config with the new anchor key automatically because relay injects the *builder*, not a pre-built config. Removed from T2.d's scope.

### T2.e — Handler fan-out optimization

Refactor the handler-building loop at `env_context_impl.go:268-277`. Today: per `(credential, filter, stream provider)`. After: per `(filter, stream provider)`, with the handler resolving the credential from the request at serving time.

At Block-scale (50 credentials × 10 filters × 4 stream providers), this is the difference between 2,000 handlers per env and 40 per env. See §6 below for the math.

### T3.a — `EnvironmentRep` extension

Add the new array fields and the canonical wire-types comment (see [`phase1-design.md`](./phase1-design.md) §5 for the comment text). Verify `DisallowUnknownFields` is not used in the env-parse path (additive-payload guarantee depends on this). Check whether `archive_reader.go` does its own parsing or consumes `EnvironmentRep` directly (T3.a's scope expands if it parses on its own).

### T3.b — Shared reconcile helper

A new helper (in `internal/envfactory/` or similar) that both `autoconfig_actions.go` and `filedata_actions.go` call. Responsibilities:
- Diff the old accepted set against the new one (set-keyed by `value`).
- Detect re-anchor (`sdkKey.value` changed).
- Compute the ordered operation list: `add → re-anchor → remove`.
- **Signal malformed-payload condition** (anchor `value` not in `sdkKeys[]`) as a structured error so the caller can both preserve the previous state *and* trigger an RAC stream reconnect with jitter (design §9).
- Treat the legacy `sdkKey.expiring{}` field as write-only — read only the array.

### T3.c — Wire both action handlers

Replace `UpdateCredential` calls with the new `ReconcileCredentials` API, via the shared helper. RAC handler and offline handler updates land in one PR (separate commits per Aaron's preference).

**Malformed-payload handling** (design §9): when the shared helper signals a malformed payload, the RAC handler must (a) preserve the previous accepted set and (b) **disconnect and reconnect the RAC stream with jitter** to force a fresh `put` from the backend. The offline handler preserves state only (no equivalent reconnect since there's no live connection — wait for the next archive reload).

Test matrix (covered in T3.c's acceptance criteria):
- Add a new key
- Set `expiry` on a non-anchor key
- Set `expiry` on the anchor (re-anchor triggered)
- Remove a key (omit from next patch)
- Rename a key (same `value`, different `key` identifier — no-op for creds)
- De-expiry (remove `expiry` on existing entry — cancel scheduled drop)
- Mixed patch (add + re-anchor + remove)
- Partial-failure reconcile (preserves previous state)
- **Malformed payload triggers RAC reconnect** (RAC handler only); state preserved meanwhile

### T4 — Status endpoint arrays

Add `sdkKeys` / `mobileKeys` array fields to the env status response. Each entry: non-secret `Key` identifier + obscured `Value` (via `sdks.ObscureKey`) + optional `Expiry`. Keep scalar `sdkKey` / `mobileKey` — they now represent the **anchor** specifically. Keep `expiringSdkKey` for default-rotation back-compat.

Stable ordering of array entries: anchor first, then identifier-alphabetical. Predictable for tooling consumers.

Arrays are *present but empty* (not omitted) for single-key envs.

### T5.a — Integration test harness

Build a reusable harness:
- **RAC mock**: emits captured payloads, supports `put`/`patch`/`delete` event sequences.
- **Downstream SDK simulator**: simulates an SDK connecting with a credential and consuming a stream.
- **Archive fixture loader**: loads offline-mode archives from disk for the filedata path.

The harness lands as Wave 1 infrastructure; scenarios accumulate as acceptance tests in the sub-tasks that introduce each feature.

### T5.b — Events payload regression test

Capture upstream payloads from v8 under realistic SDK traffic. Assert post-Phase-1 payloads are structurally identical *except* for the credential field. Catches accidental schema drift throughout the project.

### T5.f — Code cleanup before v8 merge

Remove all project-specific scaffolding from `feat/concurrent-keys` *before* T5.g merges the branch to v8. The canonical design + plan docs and the PoC test file were useful during development; they shouldn't land on v8.

What to remove:
- `.agent-docs/concurrent-keys/` — entire directory (this file is one of the things being removed). Save off-branch if you want to keep it for reference.
- `internal/relayenv/env_context_reanchor_test.go` — PoC test file. Verify any useful tests have already been adopted into proper regression test files by T2.c before deleting.
- Any other concurrent-keys-specific scaffolding that may have accumulated.

What stays:
- Actual feature code.
- Regression tests in properly-named test files (those aren't scaffolding).

Single PR. Conventional commit: `chore(concurrent-keys): remove project scaffolding before v8 merge`.

### T5.g — Merge `feat/concurrent-keys` to v8 + publish release

Final merge. **Squash-merge** as a single `feat:` commit — that commit is what release tooling sees, so it triggers the minor version bump. Suggested squash title: `feat(concurrent-keys): support multiple SDK keys per environment via RAC and offline archive`.

Steps:
1. Final review of feature branch HEAD; confirm cleanup (T5.f) is in.
2. Squash-merge `feat/concurrent-keys` → v8.
3. Verify release tooling triggers a minor version bump.
4. Publish release notes (three items from §8).

Not a coding task — this is a release activity.

### T5.e — Merge-forward to v9

Not a `git merge`. Real integration work resolving FDv2 ↔ Phase 1 interactions in `env_context_impl.go` and the streaming path. v9 has FDv2 in it, which touches the same files Phase 1 changes most heavily. Validate against v9's existing test suite plus a subset of Phase 1 tests adapted for v9.

Timing: calendar-deferred. May happen weeks or months after T5.g (v8 release), depending on when v9 is ready.

---

## 6. Handler fan-out optimization (T2.e) — the math

The optimization observes: today, relay builds one HTTP handler per `(credential, filter, stream provider)` triple. All handlers in the same `(filter, provider)` slot are byte-identical except for the credential baked in at construction. If we look up the credential from the request at serving time, we share one handler per `(filter, provider)`.

Today: `handlers per env = C × F × P` where C = credentials, F = filters+1, P = stream providers.
After: `handlers per env = F × P`.

| Customer profile | C | F | P | Unoptimized | Optimized |
|---|---|---|---|---|---|
| Single-key today (baseline) | 3 | 1 | 4 | 12 | 4 |
| Mid-market Phase 1 multi-key | 8 | 1 | 4 | 32 | 4 |
| **Block-scale (multi-key + multi-filter)** | **50** | **10** | **4** | **2,000 per env** | **40 per env** |

At ~500 bytes per handler closure, Block-scale unoptimized is ~5 MB; optimized is ~100 KB. Memory itself isn't catastrophic, but secondary costs (setup time on every reconcile, GC pressure, per-request lookup overhead) add up. Block is the named customer driver for this project; shipping unoptimized risks regressing memory characteristics for the customer the project is meant to help.

We're not gating T2.e on an empirical memory benchmark — the napkin math is sufficient justification.

---

## 7. Test strategy

### Per-PR

Every sub-PR runs the full existing test suite via existing CI. The "single-key behavior unchanged" invariant is the testable property.

**Code-review norm** (replaces the dropped T5.d CI job): feature-branch PRs must run the full test suite. Any test that needs to be removed or modified during Phase 1 must be explicitly justified in the PR description.

### Distributed tests

Each sub-task's acceptance criteria include unit and scoped-integration tests for that sub-task. Examples:

- T1.0: panic-removal unit test.
- T1.a: data-structure tests.
- T1.b: `ReconcileCredentials` unit + integration tests.
- T1.c: cleanup-ticker tests, including per-key expiry and mobile-key disconnect.
- T2.c: re-anchor integration tests (evolved from PoC).
- T3.a: parse format tests + old-relay back-compat test + `DisallowUnknownFields` verification.
- T3.c: the reconcile scenario matrix (add, set-expiry, remove, rename, de-expiry, mixed, partial failure).
- T4: status endpoint scenario tests.

### Cross-cutting tests

These live in T5 and run continuously:

- **T5.a (test harness)**: enables the per-sub-task tests above.
- **T5.b (events payload regression)**: catches schema drift in event payloads.

### Release-readiness checklist

Before merging `feat/concurrent-keys` to v8 (and again before deploying to production), run through:

1. All Wave 2 sub-tasks merged and tests passing.
2. End-to-end customer-journey integration tests pass (assembled from T5.a + per-task acceptance tests).
3. Events payload regression test (T5.b) passes against the full feature branch.
4. Single-key behavior verified identical to v8's baseline via full test suite.
5. Status endpoint manually inspected for both single-key and multi-key envs.
6. Defensive payload tests: malformed RAC payload → relay logs + preserves previous state.

This is a *checklist*, not a discrete task. Touched at release readiness, not as a separate sub-PR.

---

## 8. Rollout

### Release notes

Three customer-facing items to surface:

1. **"Concurrent SDK keys are available for relays using LaunchDarkly's Relay Auto Config.** Manual configuration continues to support one SDK key, one mobile key, and one environment ID per environment. Multi-key support for manual configuration will arrive in a future major release."
2. **"Events from all SDK keys in an environment appear under the anchor key in LaunchDarkly analytics."** This is consistent with today's single-key behavior but worth calling out because the multi-key model invites the expectation that attribution would split.
3. **"Status endpoint adds `sdkKeys` and `mobileKeys` array fields** showing all accepted keys with non-secret identifiers and obscured credentials. Existing `sdkKey` and `mobileKey` scalars now represent the *anchor* key specifically (the key relay uses for its upstream connection)."

### External follow-ups

These are tracked but not part of Phase 1's task list:

- **Public docs update**: the customer docs at `launchdarkly.com/docs/home/account/environment/keys` currently say *"If you are using the Relay Proxy, it can only use the default SDK key."* Phase 1 invalidates this. Aaron's team doesn't own public docs; Aaron contacts the docs-owning team when Phase 1 is close to shipping.

### Kill switch

Phase 1 doesn't introduce a config-level kill switch for concurrent keys. Multi-key behavior is effectively opt-in at the customer level — customers who don't create additional keys in LD's UI see zero behavior change. If a critical issue surfaces, the operational mitigation is: customer rolls back to a pre-Phase-1 build, and SDKs using non-anchor keys lose connectivity until the operator updates either the relay or the LD UI.

### Customer downgrade story (open question for the team)

Tracked as Q11. Working assumption: surface in release notes; no relay-side mitigation needed beyond messaging.

---

## 9. Deferred items

These are intentional non-goals, with notes on what would trigger reconsideration:

- **Memory benchmark for T2.e** — deferred. Napkin math (§6) is sufficient justification. Revive if teammates push back without empirical data.
- **Verify-on-startup for manual-config keys** — rejected. The full reasoning is in [`phase1-design.md`](./phase1-design.md) §1 / §13.
- **Per-key event attribution** — deliberately not pursued. Would require multiplying event machinery; SDK keys are secrets and not appropriate as analytics tags. Long-term path if ever needed: a non-secret metadata header on diagnostic events. Out of Phase 1 scope.
- **Multi env-ID support** — out of scope for Phase 1, mirroring the backend tech spec which also defers client-side ID migration.
- **Phase 2 mega-stream design** — out of scope for this plan. Phase 2 will get its own design doc.

---

## 10. JIRA structure

```
SDK-2453 (Epic) — Relay Proxy Multi Keys Support
├── T0  Re-anchoring PoC                                    [Story]
├── T1  Generalize the credential model                     [Story]
│   ├── T1.0 Remove RotateWithGrace mobile-key panic        [Sub-task]
│   ├── T1.a Add Rotator accepted-set data structures       [Sub-task]
│   ├── T1.b ReconcileCredentials API + migrate + remove    [Sub-task]
│   └── T1.c Generalize cleanup ticker                      [Sub-task]
├── T2  Decouple upstream-client lifecycle                  [Story]
│   ├── T2.a addCredential anchor-only client               [Sub-task]
│   ├── T2.b GetClient returns anchor's client              [Sub-task]
│   ├── T2.c Re-anchor mechanism per PoC                    [Sub-task]
│   ├── T2.d Big-segment + httpconfig from anchor           [Sub-task]
│   └── T2.e Handler fan-out optimization                   [Sub-task]
├── T3  Plumb N keys from trusted sources                   [Story]
│   ├── T3.a Extend EnvironmentRep + verify                 [Sub-task]
│   ├── T3.b Shared reconcile helper                        [Sub-task]
│   └── T3.c Wire RAC + offline handlers                    [Sub-task]
├── T4  Status endpoints                                    [Task]
└── T5  Tests, release, and merge-forward                   [Story]
    ├── T5.a Integration test harness                       [Sub-task]
    ├── T5.b Events payload regression test                 [Sub-task]
    ├── T5.f Code cleanup before v8 merge                   [Sub-task]
    ├── T5.g Merge feat/concurrent-keys to v8 + release     [Sub-task]
    └── T5.e Merge-forward to v9                            [Sub-task]
```

**Wave labels**: `wave-1`, `wave-2`, `wave-3` on every sub-task (and `wave-1` on T0 since it has no sub-tasks).

**Dependencies**: modeled via JIRA `blocks` links. See §4 above for the full graph.

---

## 11. Quick reference

| Question | Answer |
|---|---|
| Feature branch? | `feat/concurrent-keys` off v8 |
| Sub-PR branches? | `aaronz/<sub-task-ticket-id>/<task-slug>` off the feature branch (use the specific sub-task ticket ID, not the epic SDK-2453) |
| Where do canonical docs live? | This file + `phase1-design.md` in `.agent-docs/concurrent-keys/` on the feature branch |
| Where do working notes live? | `docs/agents/phase1-*.md` in the design worktree (gitignored, not on this branch) |
| How is ordering enforced within a `keys change` event? | Add → re-anchor → remove (atomic) |
| What triggers re-anchor? | `sdkKey.value` changed |
| Trusted sources for additional keys? | RAC + offline archive only. Manual config = single-key. |
| Events attribution? | Anchor per kind (collapse). No per-key attribution. |
| Test invariant at every PR boundary? | Single-key behavior identical to v8 |
| Where's the rationale for X decision? | See `phase1-design.md` §13 (Recorded decisions) |
| Open questions still pending? | `phase1-design.md` §14 — Q5, Q6, Q7, Q8, Q11 |
