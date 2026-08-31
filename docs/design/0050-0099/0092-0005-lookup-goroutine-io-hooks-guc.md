# Design 0092-0005 — gate client-driven I/O hooks behind a GUC

**Status:** authoritative for M0092-0005 implementation.
**Milestone:** [M0092](../../milestones/0092-lazy-row-emission-in-scan-and-project.md).

## Problem

M0091-0001 closure-captured `reg + pidStr` in the 4 hot
frame-reader/writer hooks in `serveConn`, eliminating the
`runtime.Stack`-based `activity.LookupGoroutine` from THOSE
sites. 14 remaining sites in `internal/initdb/open.go` still
use the old pattern:

- `Pool.OnPinWait` / `OnPinDone` (lines 478, 484)
- `Manager.OnReadWait` / `OnReadDone` (lines 830, 835)
- `Manager.OnWriteWait` / `OnWriteDone` (lines 847, 852, 857, 862, 867)
- `Manager.OnExtendWait` / `OnExtendDone`
- `AIO.OnWaitStart` / `OnWaitEnd`

Each fires from a CLIENT goroutine when the corresponding
I/O event occurs. Closure-capture doesn't work because the
hook closure is set up ONCE at `Open()` time, then SHARED
across all client goroutines. The hook needs to know which
client is calling — that's exactly what
`LookupGoroutine` (runtime.Stack) provides.

## Approach

**Gate these hooks behind a session-level
`track_io_timing` GUC (default off, mirroring PG's
default).** When the GUC is off, the hooks become no-ops:
they don't call LookupGoroutine, don't record wait events,
don't allocate.

When the GUC is on (diagnostic mode), the existing
LookupGoroutine path runs.

Two implementation styles:

- **(A) Per-call branch.** Hook body:
  ```go
  pool.OnPinWait = func() {
      if !cfg.TrackIOTiming { return }
      if reg, pid := activity.LookupGoroutine(); reg != nil { ... }
  }
  ```
  Branch overhead per fire (~1 ns) even when off.

- **(B) Install/remove hook based on GUC.** When GUC is
  off, install a `nil` (or a no-op closure that the storage
  layer skip-checks via `if hook != nil`). When on, install
  the LookupGoroutine-based hook. Zero per-call cost when
  off.

Recommend **(B)**. The storage layer's `Manager` /
`Pool` already nil-check these hooks (per the existing M0042
hooks pattern); installing nil when the GUC is off is the
lowest-overhead path.

GUC handling on SET (server-side): when a session sets
`track_io_timing = on`, install the LookupGoroutine-based
hook for THAT session's storage handles. Sessions in goopg
share the global storage manager, so this is a process-wide
toggle. Document that limitation; it matches PG's
process-level cluster GUC (`SET` on session affects that
backend).

Simplest scope for M0092-0005: process-wide, default off,
NO runtime SET support yet — only via postgresql.conf at
startup. Test sessions that need wait-events can enable it.

## Risk

- Existing tests that read pg_stat_activity wait events
  for I/O may break (the GUC default flips). Audit:
  - `internal/initdb/backend_goroutine_test.go` (likely
    needs the GUC on).
  - Any test that exercises `WaitEventStart`/`End` directly.
- The GUC's process-wide nature breaks PG-equivalent
  per-session behaviour. Document in the design + mark as
  M0093 candidate for proper per-session wiring.

## Test coverage

- Test toggling the GUC: default off → hooks no-op;
  postgresql.conf `track_io_timing = on` → hooks fire
  WaitEventStart/End.
- Existing wait-event tests get the GUC on via the
  postgresql.conf for the test fixture.

## Expected impact

- 14 hooks no longer call LookupGoroutine in the default
  config. Each fire saved ~µs of runtime.Stack + 64 B + 1
  string allocation.
- For pgbench's hot path (Pin per query): saves the per-
  Pin overhead. M0091-0001 already saved ~11 % of CPU at
  the 4 server hooks; M0092-0005 expected to save ~5 % more
  via the I/O hooks.
