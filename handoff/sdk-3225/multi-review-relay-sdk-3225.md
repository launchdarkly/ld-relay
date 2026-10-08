# Multi-agent review: relay client write deadline (SDK-3225), round 1

> Reviewed at `97522a32`, before a rebase onto `v9` @ `022f9189`. The same two commits are now
> `c1da9634` and `3afd80a0` on `mk/sdk-3225/stream-write-deadline`; the code is unchanged.

Branch `mk/sdk-3225/stream-write-deadline` at `97522a32` (two commits on `v9` @ `470672cf`):
`9b58cf41` initwrite cumulative accounting and stream shape, `97522a32` `CLIENT_WRITE_*` wiring.
Reviewed 2026-10-07 by three agents (general, security, adversarial). I reran every proof test
listed at the end, and each one reproduced its finding.

## Repos used

- ld-relay (`mk/sdk-3225/stream-write-deadline` @ `97522a32`):
  `/home/mkeeler/code/launchdarkly/ld-relay.git/stream-write-deadline`
- eventsource v1.14.0 (the version relay pins), read from `GOMODCACHE`
- Go 1.26.5 `net/http` and `h2_bundle.go`, read from `GOROOT`

## Summary

There are no Critical findings. Three Medium findings are proven, and two of the three reviewers
found the first one independently:

1. A poll response's final flush has no deadline.
2. A burst of live events can cut a client that reads at twice the floor.
3. The HTTP/2 `WriteByteTimeout = slack` setting is stricter than the per-unit budget, and when
   it fires it closes the whole connection.

The stream paths otherwise held up. No unit is left without a closing flush, there is no
`Begin` race, nothing touches the connection after the handler returns, no route has two
writers, and the composition with `withCloseConnection` works.

## Findings

| # | Finding | Source | Severity | Verdict |
| --- | --- | --- | --- | --- |
| 1 | Poll/eval final flush is unbounded (HTTP/1 and HTTP/2) | general, security, adversarial | Medium | PROVEN |
| 2 | Live burst cuts a client reading at 2x the floor | adversarial | Medium-High | PROVEN (needs a large autotuned sndbuf) |
| 3 | HTTP/2 `WriteByteTimeout = slack` cuts a client HTTP/1 tolerates, and kills the whole connection | adversarial, security (L1) | Medium | PROVEN |
| 4 | `CLIENT_WRITE_SLACK` has no lower bound | security | Low | by inspection |
| 5 | Flags-only `/flags` stream serializes its put inside the unit | general | Low | by inspection |
| 6 | Watcher `Cut` overwrites `Exit`'s slack on a mid-unit normal return | adversarial | Low | PROVEN |
| 7 | 503 shed (and selector 401/CORS) replies have no deadline | general, security | Low | by inspection |
| 8 | Deadline-set failures are invisible on the new paths | security, general | Low | by inspection |
| 9 | `withWriteDeadline` passes `Limits{}`, so `Begin` would divide by zero | general, security | Low | latent, unreachable |
| 10 | `endUnit` skipped by a `Begin` race leaves the old deadline until the first delivery write | general | Low | CANNOT PROVE (no reachable path) |
| 11 | Test gaps: limiter + unit limits not tested idle or on HTTP/2, route test misses goals and REPORT evalx, no `http2WriteByteTimeout` unit test | general | Low | — |
| 12 | Comment and doc errors | general, security, adversarial | Info | — |
| 13 | Embedders with their own `http.Server` get no HTTP/2 connection timeout, and their `WriteTimeout` is overwritten | security, general | Info | — |
| 14 | Watcher `Cut` after a failed HTTP/2 write sends a stray RST | general | Info | inherited from v9 |

### 1. Poll/eval final flush is unbounded

`middleware.WriteDeadline`, `LimitConcurrency` and `ProvideInitLimiter` all
`defer clearWriteDeadline(w)` (`internal/middleware/concurrency.go`). That defer runs before
net/http writes the buffered response:

- **HTTP/1:** the write happens in `finishRequest`. The response buffer is about 2 KiB and the
  conn bufio about 4 KiB.
- **HTTP/2:** it happens in `handlerDone` -> `Flush`, from a 4 KiB handler buffer.

So a poll under about 6 KB never writes to the socket while a deadline is armed, and a larger
one loses the bound for its tail. Under `EnableCompression`, gzhttp's `Close` also writes after
the clear. Two triggers park a handler goroutine indefinitely:

- a keep-alive client that pipelines and never reads;
- an HTTP/2 client with `SETTINGS_INITIAL_WINDOW_SIZE=0` that keeps reading TCP, which also
  stops `WriteByteTimeout` from firing.

This existed in v9 for `LimitConcurrency`; this change extends it to every poll route.

**Fix.** Do what streams already do: replace the clear with an exit deadline of now + slack,
or now if the request context is done. net/http clears the deadline itself after
`finishRequest` (`server.go:2081`). On HTTP/2 a deadline that is still armed when the stream
closes is stopped in `closeStream`, so it is safe to leave there. Skip it if the context is
done, which avoids the stray RST.

### 2. A burst of live events cuts a client that reads at twice the floor

Each live event is its own unit. `endUnit` clears the deadline as soon as the bytes reach the
kernel, not when the client has read them. The next unit resets `written` and `msgStart`, so
its budget covers only its own bytes, while its write waits behind the backlog that earlier
units left in the send buffer.

Proof: 2200 x 2 KiB events with floor 64 KiB/s and slack 5s, and a client reading at
128 KiB/s throughout. Event 2044 fails after 5.07s. The trigger is a burst of publishes (an
FDv2 `SetBasis` publishes once per flag, or a bulk FDv1 delta) to a client on a slow link. It
needs a large autotuned send buffer (2.9 to 4.2 MB here) and a burst that does not first
overflow eventsource's 128-event subscriber buffer.

**Proposed fix (my own).** Account per connection, not per unit. Keep a virtual drain time that
says when a client at the floor would have read everything sent so far:
`drainBy = max(drainBy, now) + n/floor` before each chunk. The deadline is then
`drainBy + slack`, capped per unit by `MaxHold`. A unit that starts while earlier bytes are
still due inherits their debt, and an idle stream lets `drainBy` fall into the past, so idle
streams are unaffected. Within a single unit this equals the current formula.

### 3. HTTP/2 `WriteByteTimeout = slack` is stricter than the unit budget

`WriteByteTimeout` closes the connection after `slack` with no TCP progress. It ignores the
cumulative budget and takes down every stream on the connection. Proof: a 3 MiB poll (budget
about 49s) whose client stops reading the socket for 3s with slack 1s. The connection survives
on HTTP/1 and is reset at 6.3s on HTTP/2.

**Options.** Corbin's #882 used his per-write maximum time. Choices:

- `CLIENT_WRITE_MAX_TIME` when it is set, and a separate fixed value (for example 30s) when it
  is not;
- a dedicated option;
- slack times a factor.

This is a design decision for you.

### 4. No lower bound on `CLIENT_WRITE_SLACK`

`config_validation.go` rejects only values of zero or less. A 1ms slack is accepted, and it
also becomes the server-wide HTTP/2 timeout (finding 3). Suggest a minimum of 1s, or a warning.

### 5. The flags-only stream serializes its put inside the unit

`MakeServerSideFlagsOnlyPutEvent` is lazy, and `getReplayEvent` never forces `Data()`.
eventsource writes `event: put` before it calls `Data()`, so the unit's clock runs during
serialization. On HTTP/2, a serialization longer than the slack resets a healthy stream.
**Fix:** force `Data()` inside the singleflight, as `serializePutV1` already does.

### 6. The watcher's `Cut` overwrites `Exit`'s slack

In `serveWithCut`, the exit defer runs first (now + slack). The deferred `cancel` then wakes
the watcher, whose `Cut` sets the deadline to now because a unit is still active. This happens
on a MaxConnTime exit mid-batch, or on a gated delivery that has begun. The connection then ends
abruptly, which contradicts the comment. Proof: "exit deadline in 5s, final deadline in 0s".
**Fix:** stop the watcher before `Exit`, or have `Exit` mark the writer so a later `Cut` leaves
it alone.

### 7 to 14

- **7.** Wrap before acquiring a slot, or soften the "every SDK poll response" claim in the docs.
- **8.** Warn once, or count, when `DeadlineSetErrors` grows on the stream and poll paths.
- **9.** Pass `DeliveryLimits(0, unit)` instead of `Limits{}`, or guard `budget` against a zero
  rate.
- **10.** Clear the deadline in `Begin` on the stream shape, or document the invariant.
- **11.** Add the limiter variant, including HTTP/2, to the idle-stream test. Add goals and the
  REPORT evalx routes to the route test. Add a unit test for `http2WriteByteTimeout`.
- **12.** Fix the comments and docs:
  - The `clearWriteDeadline` comment ("net/http does not reset it") is false since Go 1.20.
  - The initwrite poll-shape comment says net/http resets the deadline when the handler
    returns. It resets it after `finishRequest`.
  - The design doc names `/sdk/latest-all`, which does not exist; the route is `/sdk/flags`.
  - Gated deliveries ignore `CLIENT_WRITE_SLACK` and `CLIENT_WRITE_MAX_TIME`. This is
    documented, but it is worth stating more plainly.
- **13.** Add a docs line for embedders.
- **14.** The stray RST is harmless. Find 1's context check covers the poll side.

## Checked and dismissed

- **A unit that never ends with a flush:** eventsource v1.14.0 flushes after the headers, after
  each live event and comment, on the jitter path, at the end of each batch, and in `reportExit`.
  Only a connection that is already ending leaves a unit open.
- **Producer gaps billed to the client:** FDv1 forces `Data()` inside the singleflight, and FDv2
  events are pre-rendered. The only lazy case is the flags-only put (finding 5).
- **`Begin` racing a flush:** `readMainCh` is nil in batch mode, the batch is enqueued ahead of
  any live event, and the header flush finishes before registration. `-race -count=30` passes.
- **Connection touched after the handler returns:** the watcher is joined before return, and
  `Exit` is gated on HTTP/1.
- **Routes:** every flag-data route is wrapped exactly once, and keep-alive reuse is safe.
- **`withCloseConnection`:** the close function cancels the outer context, so the watcher cuts
  any blocked write.
- **Slow drip below the floor:** the cumulative count defeats it within a unit (but see
  finding 2 across units).
- **Credential in logs:** this change adds no log lines.
- **Config validation for embedders:** `NewRelay` calls `ValidateConfig`.

## Proof tests added (untracked, not committed)

All of them reproduce their finding. Run `git clean -n internal/*/proof_*` to list them.

- `internal/middleware/proof_poll_final_flush_test.go`: finding 1, a 1618-byte socket write
  under a zero deadline.
- `internal/middleware/proof_h1_poll_exit_flush_test.go`: finding 1, HTTP/1 pipelined client
  parked in `finishRequest`.
- `internal/middleware/proof_h2_poll_exit_flush_test.go`: finding 1, HTTP/2 zero-window client
  parked in `handlerDone`, with a control test.
- `internal/initwrite/proof_backlog_next_unit_test.go`: finding 2 (33s).
- `internal/middleware/proof_h2_wbt_pause_test.go`: finding 3, with the HTTP/1 control (27s).
- `internal/streams/proof_exit_then_cut_test.go`: finding 6.
