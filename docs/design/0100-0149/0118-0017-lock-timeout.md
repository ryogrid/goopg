# 0118-0017 — `lock_timeout` and statement-timeout-aware lock waits (M0118-0009 slice: timeouts)

Status: accepted

## Summary

Implements PostgreSQL's `lock_timeout` GUC and corrects the error message a
statement emits when it is cancelled while waiting for a lock. Both the
heavyweight lock manager (`LOCK TABLE`) and the transaction-XID waiter
(`WaitForXID`, used by every `UPDATE`/`DELETE`/`MERGE`/`SELECT … FOR …` that
blocks on a concurrent writer) now honour `lock_timeout` and abort the
statement with the exact upstream messages:

| cause | message | SQLSTATE |
|-------|---------|----------|
| `lock_timeout` elapsed while waiting for a lock | `canceling statement due to lock timeout` | 57014 |
| `statement_timeout` elapsed | `canceling statement due to statement timeout` | 57014 |
| client `CancelRequest` / connection teardown | `canceling statement due to user request` | 57014 |

Before this change, **all three** mapped to "canceling statement due to user
request", and `lock_timeout` was not enforced at all (the GUC parsed and
stored but never armed a timer). Row-level lock waits additionally *swallowed*
the cancellation entirely — `epqWait` did `_ = WaitForXID(...)` and the
EvalPlanQual loop retried until it spuriously raised `40001` (could not
serialize), so a `statement_timeout` on a blocked `DELETE` surfaced the wrong
SQLSTATE *and* the wrong message.

This is the row-level half of
`postgres/src/test/isolation/specs/timeouts.spec`. The table-level half is
deferred (see "Deferred" below).

## Oracle

`lock_timeout` mirrors `postgres/src/backend/storage/lmgr/proc.c` (`ProcSleep`
arms `enable_timeout_after(LOCK_TIMEOUT, …)` at the start of each lock wait and
disables it once the lock is granted) and `postgres/src/backend/tcop/postgres.c`
`ProcessInterrupts` (`LockTimeoutPending` → `ereport(ERROR, …, "canceling
statement due to lock timeout")`). Documented semantics:
`postgres/official_docs_in_md` — `lock_timeout` "Abort any statement that waits
longer than the specified amount of time while attempting to acquire a lock …
measured from the time a lock-wait begins", distinct from `statement_timeout`
which is measured from statement start.

## Design

### Budget carried as a context value, not a deadline

`lock_timeout` cannot collapse into the statement context's deadline because
the two clocks differ (statement-start vs lock-wait-start) and the wait
primitive must report *which* fired so the dispatcher emits the matching
message. A new leaf package `internal/lockwait` carries the budget down through
the request context and defines the sentinel:

- `lockwait.WithTimeout(ctx, d)` — attach a positive lock-wait budget.
- `lockwait.Timeout(ctx)` — read it.
- `lockwait.ErrLockTimeout` — returned by a wait primitive when the budget
  elapses.

The budget is a **duration**, not an absolute deadline, so each individual
lock wait re-arms its own timer from the moment that wait begins (matching
upstream's per-`ProcSleep` arming). `internal/lockwait` imports only the
standard library, so both `lockmgr` and `mvcc` (which sit below `executor`)
can depend on it without an import cycle.

### Wiring

- **dispatch.go** (`handleSimpleQuery`): the statement context already gets a
  `context.WithTimeout` deadline when `statement_timeout > 0`; it now *also*
  gets `lockwait.WithTimeout` when `lock_timeout > 0` (independently — a
  `lock_timeout` with no `statement_timeout` still arms). New helper
  `sessionLockTimeout` mirrors `sessionStatementTimeout`.
- **lockmgr.acquire**: the parked-waiter `select` gains a `lock_timeout` timer
  case alongside the existing deadlock-timer and `ctx.Done()` cases. On expiry
  it unparks (the splice-and-repair bookkeeping shared with the `ctx.Done()`
  path, factored out as `unparkWaiter`) and returns `lockwait.ErrLockTimeout`.
- **mvcc.WaitForXID**: arms a `time.AfterFunc` that broadcasts the wait cond;
  the wait loop checks `ctx.Err()` first (statement timeout / cancel) then the
  lock deadline, returning `lockwait.ErrLockTimeout`.

### Error mapping

Two helpers in `executor/context.go`:

- `lockWaitTimeoutError(err)` — maps **only** the two timeout causes
  (`ErrLockTimeout`, `context.DeadlineExceeded`) to their 57014 `ExecError`;
  returns nil for a plain `context.Canceled`.
- `lockWaitCancelError(err)` — the superset that additionally maps
  `context.Canceled` to "user request"; used by the `LOCK TABLE` /
  `acquireRelLock*` sites where a client cancel *should* surface.

`statement_timeout` is the only source of a context *deadline* in goopg, so
`context.DeadlineExceeded` is unambiguously a statement timeout.

### Row-level waits: propagate timeouts, keep swallowing plain cancel

`epqWait` (the EvalPlanQual wait shared by all `operators_storage.go` /
`operators_merge.go` write paths) now returns a second result
`timeout *ExecError`. When the wait is aborted by a `lock_timeout` /
`statement_timeout`, every one of its ~11 call sites surfaces that error
(tagged with the op's position) instead of looping into a spurious `40001`. A
plain client cancellation is still swallowed (`false, nil`) so connection
teardown falls through to the historical snapshot-refresh + retry. The four
`lockRowsOp` `WaitForXID` sites (`SELECT … FOR UPDATE/SHARE` and the chain
walk) get the same treatment via `rowWaitTimeoutError`.

## Hot-path safety

`lock_timeout` defaults to 0 (off): when unset, no timer is armed and no
context value is attached, so the pgbench/TPC-H paths are byte-for-byte
unchanged. Verified: pgbench TPC-B + select-only smoke, 0 failed transactions,
~14.5k TPS select-only (unchanged).

## Gates

- `TestPort_TimeoutsRowLevel` (new) — 4 sub-cases: `statement_timeout`,
  `lock_timeout`, `lock_timeout_wins` (shorter lock_timeout fires first),
  `statement_timeout_wins` — all assert the correct message and that the abort
  lands near the 300 ms budget (not instantly, not only at holder rollback).
- `TestLockManagerLockTimeoutAbortsWait` / `…NoFalsePositive` (new lockmgr
  unit) — `-race`.
- Regression: the full row-lock / savepoint / merge / multixact / deadlock /
  skip-locked / nowait isolation-port batch — no regression.
- `-race` on `executor`, `mvcc`, `lockmgr`; executor unit suite PASS.
- pgbench smoke 0-failed.

## Deferred

The **table-level** half of `timeouts.spec` (the `rdtbl … locktbl(*)`
permutations) requires a plain `SELECT` to take a transaction-scoped
`ACCESS SHARE` heavyweight lock so a later `LOCK TABLE … (ACCESS EXCLUSIVE)`
conflicts with it. goopg deliberately keeps ordinary scans **off** the
heavyweight lock manager (see `tableLockMgr` rationale in
`executor/context.go`: only explicit `LOCK TABLE` touches it, to confine
blocking off the pgbench/TPC-H hot path). Adding `ACCESS SHARE`-on-scan is a
separate, perf-sensitive design decision and is recorded in the deferral
ledger; until it lands, `timeouts.spec` is not promoted to `port` in the
target inventory. The `lock_timeout` machinery added here already covers the
`LOCK TABLE` wait, so that half needs only the scan-side lock acquisition.
