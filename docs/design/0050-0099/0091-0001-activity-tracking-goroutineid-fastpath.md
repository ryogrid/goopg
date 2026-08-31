# Design 0091-0001 — activity.goroutineID fast-path

**Status:** authoritative for M0091-0001 implementation.
**Milestone:** [M0091](../../milestones/0091-select-only-tps-regression-recovery.md).

## Problem

pprof of `pgbench -S -c 10 -j 10 -T 60` against goopg shows
`activity.LookupGoroutine` consuming **11.42 % of CPU** (CPU
profile cumulative). Inside it, `activity.goroutineID()` at
`internal/activity/activity.go:308-322` calls
`runtime.Stack(buf, false)` on every wait-event hook fire,
plus allocates a 64-byte buffer and a string per call.

Hot-fire sites (per Explore audit):

- `internal/server/server.go:590,595,600,605` — frame
  reader/writer hooks fired on every TCP packet (most
  frequent — multiple per query).
- `internal/initdb/open.go:479,484,830,835,847,852,857,862,867,872,877,882,890,895`
  — buffer-pool / Manager / WAL writer / AIO / checkpointer
  hooks fired per I/O.
- `internal/executor/spill.go:40,104` — per row written /
  read from spill files (cold path).

The Registry + PID returned by `LookupGoroutine` are known at
goroutine-registration time:

- `cmd/goopg/main.go:471` — checkpointer registers
  `(act, cpPID)`.
- `internal/server/server.go:578` — `acceptLoop.func1`
  per-connection goroutine registers `(reg, pidStr)`.
- `internal/initdb/open.go:231` — WAL writer goroutine
  registers `(act, "wal-writer-0")`.

These registration sites already have the registry pointer
and the PID in scope. The 4 hottest call sites
(`server.go:590-605`) are inside the same `serveConn`
goroutine that did the registration — so they can capture
`reg` and `pidStr` via closure scope directly.

## Approach

**Phase 1 — Closure-capture in the hot sites (the bulk of the win):**

`internal/server/server.go::serveConn` defines 4 hook closures
that fire on every TCP read/write boundary. At the moment they
each call `activity.LookupGoroutine()` which does a
`runtime.Stack`. Since `reg` and `pidStr` are in the enclosing
function's scope (variables declared at line 557-578 above
the hook definitions at line 590-605), the hooks can reference
them directly.

```go
// Before:
OnBeforeRead: func() {
    reg, pid := activity.LookupGoroutine()
    if reg != nil {
        reg.WaitStart(pid, activity.WaitTypeClient, activity.WaitClientRead)
    }
},
// After (M0091-0001):
OnBeforeRead: func() {
    if reg != nil {
        reg.WaitStart(pidStr, activity.WaitTypeClient, activity.WaitClientRead)
    }
},
```

Same pattern for `OnAfterRead`, `OnBeforeWrite`, `OnAfterWrite`.

**Phase 2 — WAL writer + checkpointer hooks:**

In `internal/initdb/open.go`, the WAL writer's `OnLoopStart`
(line 231) calls `RegisterCurrentGoroutine(act, "wal-writer-0")`.
The hooks that fire while inside the WAL writer's goroutine
(`OnAppendWait`, `OnFlushWait`, etc.) can closure-capture
`act` and the literal PID `"wal-writer-0"`.

Same for the checkpointer hooks at lines 890, 895 (the
checkpointer's PID is `cpPID` set at `cmd/goopg/main.go:471`,
known when `Open()` is called).

**Phase 3 — Client-driven Manager / Pool / AIO hooks:**

The trickier set. `Manager.OnReadWait`, `Pool.OnPinWait`,
`AIO.OnWaitStart`, etc. fire from CLIENT goroutines (the
connection backends). The hook is shared across all clients;
closure-capture won't work because there is no single
`(reg, pid)` to capture at hook-setup time.

For these, the current per-call `LookupGoroutine` does the
right thing semantically. The cheapest fix in M0091 scope is
to **leave these as-is** — the per-query cost contribution
is much smaller than the server.go hot path (~10× fewer fires
than read/write hooks at TCP-packet granularity), and a clean
plumbing fix (Pool/Manager carrying a per-call Registry+PID)
is a larger refactor.

If post-fix measurement shows these are still material, a
follow-up can:
(a) Plumb `*activity.Registry` and `string PID` through
    `Pool.Pin`, `Manager.ReadBlock`, etc. as explicit
    arguments. Big API surface change.
(b) Use a goroutine-local id via `unsafe`/`runtime.linkname`
    on `getg`. Avoids API change, uses unsafe.
(c) Add a fast caching layer: at `RegisterCurrentGoroutine`
    time, compute the goroutineID once and store
    `(goroutineID → entry)` in the existing `goroutineMap`;
    at lookup time, derive the goroutineID via a faster
    runtime function (still runtime.Stack but with a smaller
    buf and earlier exit on first space).

These are deferred. Phase 1 alone should recover most of the
11 % CPU.

**Phase 4 — Spill sites:**

`internal/executor/spill.go:40,104` are cold-path (only fire
when an in-memory join / aggregate spills to disk). The simplest
fix is to drop the per-row `LookupGoroutine` call — the spill
wait events are too granular to be useful for pg_stat_activity
display, and the alternative (plumbing `*Context` through the
spill struct) is straightforward but unnecessary in M0091's
scope. **Decision: drop these calls** (no-op when spill
happens; pg_stat_activity loses fine-grained spill wait events
but gains throughput).

## Safety / nil-fallback contract

`activity.LookupGoroutine` continues to exist and to return
`(nil, "")` when the calling goroutine hasn't registered. The
test at `internal/initdb/backend_goroutine_test.go:41-46`
exercises this contract and continues to work — we don't
remove the function, just stop calling it from the hot sites
that already have the values in scope.

`internal/activity/` package itself is unchanged.

## Test coverage

- Existing `internal/activity/activity_test.go` tests pass
  unchanged.
- Existing `internal/initdb/backend_goroutine_test.go` tests
  pass unchanged (their use of `LookupGoroutine` for
  assertions still works).
- Existing `internal/server/` tests that depend on wait-event
  recording via the server hooks continue to work — the
  hooks still call `WaitStart` / `WaitEnd` with the right
  pid; only the lookup mechanism changes.

## Expected impact

- ~11 % of CPU recovered (the runtime.Stack call eliminated
  from the 4 hottest sites).
- Reduced allocation rate: no more 64-byte buf + string
  alloc per hook fire (~4 KB/query at -c 10).
- GC pressure drops proportionally.

The combination with M0091-0002 (btree.RangeScan zero-copy)
is expected to recover the bulk of the 17× regression.
