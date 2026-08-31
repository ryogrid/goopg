# 0118-0003 — Transaction-scoped `LOCK TABLE` heavyweight locks

**Status:** landed (slice 15 of M0118-0003; promotes the `lock-nowait` isolation
spec).

**Related:** `docs/design/0012-0001-lock-manager-architecture.md` (the `lockmgr`
package), `docs/design/0097-0095-lock-table-pg-locks-tracking.md` (the `pg_locks`
display registry), `docs/design/0118-0002-multixact-tuple-lock-subsystem.md`
(slice narrative for the M0118-0003 row-locking cluster).

## Problem

`LOCK TABLE rel IN <mode> MODE [NOWAIT]` must take a relation-level heavyweight
lock that:

1. **blocks** a conflicting `LOCK` in another session (per the upstream
   `LockConflicts[]` matrix), and
2. is held until **end of transaction** (`COMMIT`/`ROLLBACK`), not released at
   the end of the acquiring statement.

The upstream `lock-nowait` isolation spec additionally exercises the
`JoinWaitQueue` *early-grant* special case: a session that already holds a
strong, self-compatible lock may take a weaker lock **immediately** even while a
conflicting waiter is parked ahead of it — so `LOCK … NOWAIT` succeeds instead
of failing.

Before this slice, `execLockTable` recorded each lock only in the in-memory
`globalRelLockMgr` display registry that powers `pg_locks` (see 0097-0095); it
never took a real blocking lock, so `LOCK TABLE` never waited.

## Why the existing `lockmgr` wasn't enough

The `lockmgr` package (0012-0001) already provides every primitive needed: the
conflict matrix, a FIFO blocking `Acquire`, a non-blocking `TryAcquire`
(`NOWAIT`), and `canGrantImmediately`'s early-grant rule — the latter written
*explicitly* for the `lock-nowait` spec. Two facts nonetheless kept `LOCK TABLE`
from working:

- **`lockmgr` is never instantiated in the production server.** `lockmgr.New()`
  has no non-test caller, so `server.Config.LockMgr` is always `nil` and the
  `Context.acquireRelLock` / `acquireTupleLock` helpers are no-ops there.
  Cross-statement *row* blocking instead rides `xmax`/`WaitForXID`, which is why
  the tuple-lock specs pass with a `nil` `LockMgr`. Turning the global manager on
  would activate heavyweight locking for *every* scan/DML/DDL acquire site across
  the engine — far too wide a blast radius, and a real risk of new blocking on
  the pgbench/TPC-H hot paths.
- **Lifecycle is statement-scoped.** `dispatch.go` mints a fresh `BackendID` per
  `Query` message and calls `LockMgr.ReleaseAll(backendID)` at the end of each
  statement. `LOCK TABLE` must outlive the statement.

## Design

### A dedicated, always-on table-lock manager

`LOCK TABLE` gets its own `lockmgr.LockManager` singleton in the executor,
`tableLockMgr` (`internal/executor/context.go`), touched by nothing but explicit
`LOCK` statements. This confines the blast radius of real heavyweight locking to
`LOCK TABLE`: ordinary scans, DML and DDL continue to use the `nil`
`Context.LockMgr` and are completely unaffected. The production server therefore
gains `LOCK TABLE` blocking without changing any other concurrency behaviour.

### Transaction-scoped backend identity

A stable per-connection lock-manager identity carries the transaction lifetime:

- `connTxState.LockBackendID` (`internal/server/conn_tx.go`) is minted once per
  connection in `runPostStartupLoop` from the same monotonic `nextBackendID`
  counter as per-statement `BackendID`s, so it can never collide with one.
- `dispatch.go` copies it onto every `Context` as `TxnLockBackendID` **only while
  an explicit transaction block is active** (`connTx.InExplicit()`). Outside an
  explicit block it stays `0`, which makes `acquireRelLockTxn` a no-op — autocommit
  `LOCK` keeps its historical display-only behaviour and can never leak a lock
  that nothing would release.

### Acquire and release

- `execLockTable` parses the mode with `lockmgr.ParseMode` (the parser already
  emits the canonical internal spelling — `AccessExclusiveLock`, … — so no second
  translation table is needed) and threads it, plus the `NOWAIT` flag, through
  `lockRelationTransitively`.
- `lockRelationTransitively` keeps the existing `globalRelLockMgr.AddRelationLock`
  display registration **and** calls `Context.acquireRelLockTxn`, which
  `Acquire`s (blocking) or `TryAcquire`s (`NOWAIT`) on `tableLockMgr` under
  `TxnLockBackendID`. Views and inheritance children are locked transitively, as
  before.
- Because `TxnLockBackendID` differs from the per-statement `BackendID`, the
  per-statement `ReleaseAll(backendID)` in `dispatch.go` leaves the `LOCK TABLE`
  lock untouched.
- `connTxState.End()` (called on `COMMIT`/`ROLLBACK`, and on connection teardown
  via `rollbackOpenTxnOnTeardown`) calls `executor.ReleaseTableLocks(LockBackendID)`
  to drop every table lock held by the transaction — exactly transaction
  lifetime.

### Error mapping

- `NOWAIT` contention → SQLSTATE `55P03` (`could not obtain lock on relation`).
- A lock cycle detected by the manager's wait-for-graph detector → `40P01`
  (`deadlock detected`).
- Statement cancellation while parked → `57014`.

## `lock-nowait` walk-through

1. `s1a`: `LOCK ACCESS EXCLUSIVE` — granted immediately (no holders).
2. `s2a`: `LOCK EXCLUSIVE` — conflicts with `s1`'s `ACCESS EXCLUSIVE`; the `s2`
   backend parks in `Acquire`, the isolation runner reports `<waiting ...>`.
3. `s1b`: `LOCK SHARE ROW EXCLUSIVE … NOWAIT` — `TryAcquire`. `canGrantImmediately`
   sees that `s1` already holds a lock here (`myHeld != 0`) and that the parked
   `s2` `EXCLUSIVE` waiter conflicts with `s1`'s held mode, so `s1` would be
   inserted ahead of it; `SHARE ROW EXCLUSIVE` conflicts with neither the
   (empty) requests ahead nor any other backend's holdings, so it is granted
   immediately rather than returning `55P03`.
4. `s1c`: `COMMIT` → `End()` → `ReleaseTableLocks(s1)` → the wake-pass promotes
   `s2`'s `EXCLUSIVE`; `s2a` completes.
5. `s2c`: `COMMIT`.

Output matches PG 18.3 byte-for-byte.

## Scope / non-goals

- Only `LOCK TABLE` uses `tableLockMgr`. Wiring the *global* `Context.LockMgr` so
  that scans/DML/DDL take real heavyweight locks (and the row-lock deadlock
  detection needed by `tuplelock-upgrade-no-deadlock` / `multixact-no-deadlock`)
  remains future work in the M0118-0003 cluster.
- The `pg_locks` display registry (`globalRelLockMgr`) is unchanged; it continues
  to report `LOCK TABLE` holders for observability.

## Verification

- `go test -v -run TestPort_IsolationLockNowait ./internal/testport/` — PASS
  (byte-for-byte vs PG 18.3 expected output).
- `go test -race ./internal/lockmgr/...` and `-race` over
  `TestPort_IsolationLockNowait` / `LockCommittedUpdate` / `TuplelockPartition` /
  `PropagateLockDelete` — PASS (lock/concurrency change requires the race gate).
- `go test ./internal/executor/... ./internal/server/... ./internal/lockmgr/...`
  — PASS (no regression).
- CSV `failed`→`pass`; `postgres-oracle-target-inventory.md` and
  `upstream-isolation-coverage.md` regenerated (isolation pass 51→52).
