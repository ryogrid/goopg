# 0118-0005 — General (timeout-driven) deadlock detection for LOCK TABLE (M0118-0004 slice: deadlock-hard)

Status: accepted

## Problem

The `deadlock-hard` isolation spec builds an **8-session, 8-relation** cycle with
`LOCK TABLE`: s1 holds a1 and waits on a2 (held by s2), s2 holds a2 waits on a3,
…, s8 holds a8 waits on a1. Every session sets `deadlock_timeout = '100s'`
*except* s8, which sets `'10ms'`. Upstream's comment captures the intent:

> Since it involves more than two processes, the main lock detector will find the
> problem and rollback the session that first discovers it. Set `deadlock_timeout`
> in each session so that it's predictable which session fails.

So s8 — having the shortest timeout — runs `DeadLockCheck` first, finds the
multi-relation cycle, and is the victim (`40P01`). The expected output also relies
on the `s8a1(*)` permutation annotation to force the victim step to be reported in
`<waiting ...>` / `<... completed>` form even though its wait (10 ms) is too short
for `isolationtester` to reliably observe.

goopg already had the general wait-for-graph detector (`internal/lockmgr/deadlock.go`,
M0012-0002): it walks every waiter → conflicting-holder edge, finds a cycle by
three-colour DFS, and cancels one victim. The transaction-scoped `tableLockMgr`
(slice 15, [0118-0003](0118-0003-txn-scoped-lock-table.md)) already routes
`LOCK TABLE` through `lockmgr`. Three gaps blocked the spec:

1. **`deadlock_timeout` was not a GUC.** `SET deadlock_timeout = '10ms'` failed at
   session setup with *unrecognized configuration parameter*. The lockmgr used a
   single process-wide `deadlockTimeout` (default 1 s) with no per-session value.
2. **Victim selection didn't match PostgreSQL.** The detector always picked the
   *youngest* backend (highest `BackendID`). PG rolls back the session that *runs*
   the check (the one whose `deadlock_timeout` expired) — here s8. Youngest-backend
   selection is only coincidentally s8 and not robust.
3. **The runner ignored the `(*)` marker.** A step that completed immediately (the
   victim's fast `40P01`) was rendered as a single completed line, omitting the
   `<waiting ...>` marker the expected output carries.

## Change

### 1. `deadlock_timeout` GUC (`internal/config/defaults.go`)

Registered as `TypeInt` / `UnitMs`, `BootVal "1000"`, range `[1, INT_MAX]`,
`ContextSuset` (superuser-set, mirroring `guc_tables.c`'s `PGC_SUSET`), scope
session | transaction. Added the matching commented entry to
`internal/config/postgresql.conf.sample` (`#deadlock_timeout = 1000`) to satisfy
the `TestSampleConfigCoversRegistry` invariant.

### 2. Per-session timeout plumbed into the lockmgr

- `lockmgr.AcquireWithTimeout(ctx, b, t, m, timeout)` — Acquire with a per-call
  deadlock timeout. `Acquire` is now a thin wrapper passing the sentinel
  `useConfiguredTimeout` (falls back to the manager-wide field). A `timeout <= 0`
  disables the auto-fire timer for that acquire (unchanged `SetDeadlockTimeout(0)`
  semantics used by the synchronous unit tests).
- `executor.Context.deadlockTimeout()` reads the session-effective
  `deadlock_timeout` GUC (a bare-ms integer after `UnitMs` canonicalisation) and
  returns it as a `time.Duration`, falling back to
  `lockmgr.DefaultDeadlockTimeout` when unset/unparseable.
- `acquireRelLockTxn` (the `LOCK TABLE` path) now calls `AcquireWithTimeout(…,
  c.deadlockTimeout())` so each session waits its own `deadlock_timeout` before
  running the detector.

### 3. Firing-backend victim selection

- The parked-acquire timer now fires `runDeadlockCheckFor(b)` (was the
  parameterless `runDeadlockCheck`), passing the backend whose timer expired.
- `checkDeadlockLockedFor(prefer)` is the new core: same edge-build + DFS, but when
  `prefer != 0` *and* participates in the discovered cycle it is chosen as the
  victim; otherwise it keeps the youngest-in-cycle rule. `checkDeadlockLocked()`
  (used by `CheckDeadlocksNow`, and thus the existing youngest-victim unit tests)
  delegates with `prefer = 0`, so its behaviour is unchanged.

This mirrors PostgreSQL: the backend whose `deadlock_timeout` expires runs
`DeadLockCheck` and is the one rolled back for a hard cycle.

### 4. Runner `(*)` marker (`internal/testport/framework/isolation_runner.go`)

`hasStarBlocker(blockers)` detects the `BlockerStar` (`(*)`) annotation. In the
immediate-completion branch, a `(*)` step is now rendered in waiting/completed
form — `formatWaitingStepHeader` then `writeCompletedStep` — followed by the usual
`drainWithTimeout` so unblocked waiters (e.g. `s7a8`, gated on `s8a1` via its
`(s8a1)` `BlockerStepComplete`) still surface in upstream order. No
currently-passing spec uses `(*)`, so blast radius is nil.

## Scope / non-goals

- **Hard** (mutual-exclusion) multi-object cycles only. **Soft**-deadlock queue
  reordering (`deadlock-soft`, `deadlock-soft-2` — rearrange the wait queue to
  avoid killing anyone) is a separate slice; the detector still kills a victim
  rather than reordering.
- The **row-lock** wait graph (`xmax` / `WaitForXID`, e.g.
  `tuplelock-upgrade-no-deadlock`, `multixact-no-deadlock`) is invisible to
  `lockmgr` and remains future work.
- `deadlock-parallel` (parallel-worker deadlock) is out of scope.

## Oracle

`postgres/src/backend/storage/lmgr/deadlock.c` (`DeadLockCheck`,
`FindLockCycle`), `proc.c` (`ProcSleep` scheduling the check at
`deadlock_timeout`), `guc_tables.c` (`deadlock_timeout` = `PGC_SUSET`, 1 s
default). Behaviour compared against `./postgres/local_install` PG 18.3 via the
isolation runner.

## Verification

- `TestPort_IsolationDeadlockHard` PASS byte-for-byte vs PG 18.3.
- `go test -race ./internal/lockmgr/...` green (youngest-victim unit tests via
  `CheckDeadlocksNow` unchanged; timer-fires test still cancels exactly one
  victim).
- `go test -race -run "Lock|Deadlock" ./internal/executor/` green.
- `go test ./internal/config/...` green (GUC registered + sample entry).
- Regression: `TestPort_IsolationDeadlockSimple`, `TestPort_IsolationLockNowait`,
  `TestPort_IsolationTuplelockUpdate` still PASS.
- CSV `failed`→`pass`; coverage + inventory regenerated (isolation pass 53→54).
