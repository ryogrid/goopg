# 0021-0012 — Tuple-lock FIFO wiring (row-lock waiter fairness)

Status: **draft** (diagnosis + design; implementation deferred)

Milestone: M-NIGHTLY (AI-20260712-020530-002) → tuple-level locking (0021 family)

## Problem

`TestPort_IsolationTuplelockUpgradeNoDeadlock` is flaky (~10–17% standalone).
The failing permutation is:

```
s1_share  s2_for_update  s3_for_update  s1_rollback  s2_rollback  s3_rollback
```

Expected (PG 18.3): when `s1` releases its `FOR SHARE`, the two waiters acquire
`FOR UPDATE` in **arrival order** — `s2` (issued first) completes, then after
`s2_rollback`, `s3` completes:

```
s2_for_update: <... completed>
s2_rollback
s3_for_update: <... completed>
```

Observed on the flaky runs: `s3` (issued **second**) completes first and `s2`
then times out (`ERROR: driver: bad connection`) — a FIFO-fairness violation.

## Root cause (empirically confirmed, loop #90)

This **corrects** loop #89's diagnosis. Loop #89 claimed the gap was in the DML
`UPDATE`/`DELETE` conflict path (`epqWait` → `mvcc.WaitForXID`) "with no
serialising tuple lock", and that `FOR UPDATE` was stable "because `lockRowsOp`
DOES `acquireTupleLock`". Both halves are wrong.

Instrumenting `Context.acquireTupleLock` (a temporary `fmt.Fprintf` on the
`c.LockMgr == nil` branch) shows that **every** tuple-lock acquisition in the
server path reports `LockMgr == nil`. So `acquireTupleLock` /
`tryAcquireTupleLock` are **total no-ops in production** — including
`lockRowsOp`'s `FOR UPDATE` `ExclusiveLock` acquire at
`operators_lockrows.go:900`.

This is **by deliberate design**. `internal/executor/context.go:863-871`:

> `tableLockMgr` is the dedicated, always-on heavyweight lock manager that
> backs `LOCK TABLE`. It is deliberately SEPARATE from `Context.LockMgr` (which
> is nil in the production server, so the relation/tuple `acquireRelLock`
> helpers are no-ops there and cross-statement row blocking instead rides
> xmax/WaitForXID). Keeping LOCK TABLE on its own manager confines the blast
> radius of real heavyweight locking to explicit LOCK statements: ordinary
> scans, DML and DDL never touch it …

Consequence for the failing permutation:

1. `s1_share` stamps a lock-only xmax and (transiently) takes a `RowShareLock`
   tuple lock — but on the **nil** `c.LockMgr`, so nothing is registered. The
   lock-only xmax persists; the "lock" does not.
2. `s2_for_update` and `s3_for_update` each call `acquireTupleLock(ptr,
   ExclusiveLock)` — both no-ops, both granted instantly. Neither queues behind
   the other. Both then reach the lock-only-conflict branch
   (`operators_lockrows.go:1188`) and block on **`mvcc.Manager.WaitForXID(s1)`**.
3. `s1_rollback` wakes the xid waiters via a single `commitCond.Broadcast()`
   (`mvcc/manager.go`). **Both** `s2` and `s3` wake and race to re-stamp the
   row. Go scheduling picks the winner; when `s3` wins it stamps its lock-only
   xmax first, `s2` then waits on `s3`'s xmax (which only clears at
   `s3_rollback`, later in the permutation), so `s2`'s step times out.

PG avoids this because `heap_lock_tuple` / `heap_update` acquire a heavyweight
`LOCKTAG_TUPLE` **before** `XactLockTableWait`; the per-tuple lock's wait queue
is FIFO, so `s2` (ahead in the queue) always re-locks the row before `s3`.

### Empirical trace (abridged)

```
ATLDBG b=15 ptr={0 1} mode=ExclusiveLock LockMgrNil=true   # s2 FOR UPDATE — no-op
ATLDBG b=17 ptr={0 1} mode=ExclusiveLock LockMgrNil=true   # s3 FOR UPDATE — no-op
TLUDBG b=15 WAITFORXID hx=4        # s2 waits on s1
TLUDBG b=17 WAITFORXID hx=4        # s3 ALSO waits on s1 (no FIFO gate between them)
TLUDBG b=15 WAITFORXID-done hx=4   # s1 rolled back → both wake and race
```

No `LMDBG` (lockmgr grant/enqueue) line ever fired for a tuple tag — the lock
manager is never consulted for tuple locks in the server.

## Proposed fix

Route the **tuple-lock** helpers to the always-on package-global
`tableLockMgr` (exactly as `LOCK TABLE` already does), instead of the nil
per-context `c.LockMgr`. Scope strictly to *tuple* tags — leave
`acquireRelLock`/`tryAcquireRelLock` on `c.LockMgr` (nil → no-op) so ordinary
scans/DML/DDL keep their current relation-lock-free hot path.

Concretely, in `internal/executor/context.go`:

- `acquireTupleLock` / `tryAcquireTupleLock`: acquire on `tableLockMgr` under a
  **statement-scoped** backend identity (`c.BackendID`), not `c.LockMgr`.
- Add a **per-statement release** of `tableLockMgr` holdings under
  `c.BackendID` (mirror of the existing per-txn `ReleaseTableLocks`, but at
  Query-message end). Required so the tuple lock is released when `s2`'s
  `FOR UPDATE` statement completes — by then `s2` has stamped its lock-only
  xmax, so `s3` (next to acquire the FIFO tuple lock) blocks on that xmax until
  `s2_rollback`. The tuple lock only enforces *arrival order*; the xmax still
  enforces *"wait until the holder's (sub)txn ends"*.

Then re-promote `TestPort_IsolationTuplelockUpgradeNoDeadlock` to
`runIsoSpecStrict` and flip `postgres-oracle-target-inventory.csv` back to
`pass` + regenerate the `.md`.

## Why this is HIGH blast radius (why it is deferred, not landed here)

Activating real tuple locking in the production path — which the design
intentionally kept dormant — touches the whole isolation surface:

- **Second deadlock domain.** Row-lock deadlocks currently resolve (or hang)
  only through `WaitForXID` + the WFG in `epqWait`. Real `tableLockMgr` tuple
  locks add the lockmgr's own `deadlock_timeout` detector. A cycle that spans
  both domains (holder waits on an xid; waiter blocks on the tuple lock) is
  invisible to either detector alone — needs analysis against the deadlock
  specs.
- **NOWAIT / SKIP LOCKED double-handling.** `tryAcquireTupleLock` becoming live
  means 55P03 / row-skip can now fire from the lock manager as well as the
  existing persisted-xmax path; the two must not both trigger or change the
  error/`<waiting>` timing the isolation scheduler observes.
- **Key-share compatibility.** goopg's tuple-lock modes are coarse
  (`RowShareLock` for FOR (KEY) SHARE, `ExclusiveLock` for FOR UPDATE / NO KEY
  UPDATE) vs PG's four-mode `MultiXactStatus` compatibility matrix. Real
  locking may over-block relative to the multixact-based path that currently
  governs FOR SHARE co-holders.
- **Hot-path performance.** The design comment’s original motivation. `FOR
  UPDATE`/foreign-lock paths are not the pgbench TPC-B hot path (random `aid`
  ⇒ contention is rare), but this must be confirmed with the pgbench smoke.

## Validation plan (required before landing)

1. `go test -run TestPort_Isolation ./internal/testport/` — the **full**
   isolation suite must stay green (deadlock, rowlock-family, for-share
   multi-holder, subxact-scoped-release specs are the high-risk ones).
2. Loop `TestPort_IsolationTuplelockUpgradeNoDeadlock` ≥ 20× strict — zero
   flakes.
3. `scripts/tpch-spotcheck.sh` (Q12=2/Q13=35) + the pgbench CI-parity smoke —
   no hot-path regression.

## Resume point

`internal/executor/context.go`: `acquireTupleLock` (line ~783) &
`tryAcquireTupleLock` (line ~810) — swap `c.LockMgr` → `tableLockMgr` under
`c.BackendID`; add a per-statement `tableLockMgr.ReleaseAll(c.BackendID)` hook
(dispatch.go Query-message end, alongside the existing txn-end
`s.cfg.LockMgr.ReleaseAll`). Keep `acquireRelLock` on `c.LockMgr`. See the
deferral-ledger row dated 2026-07-12.
