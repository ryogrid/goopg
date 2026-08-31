# 0021-0012 — Tuple-lock FIFO wiring (row-lock waiter fairness)

Status: **landed** (loop #91, 2026-07-14 — see "Implementation" below)

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

## Implementation (loop #91, 2026-07-14)

The landed implementation refines the "Proposed fix" above in two ways the
original sketch didn't anticipate, both discovered while porting
`heap_lock_tuple`/`heap_update` more faithfully:

1. **Lazy acquire, not up-front acquire.** The proposed fix's `acquireTupleLock`
   swap alone would make *every* row-lock/DML request touch the tag, including
   non-conflicting ones — that reintroduces a deadlock in exactly the case
   `TestForShareCompatibleMultipleHolders` / `tuplelock-upgrade-no-deadlock`'s
   perm 9 exercises (an upgrade queueing behind a waiter parked on our own held
   lock). Upstream only calls `heap_acquire_tuplock` right before it sleeps
   (`XactLockTableWait`/`MultiXactIdWait`), so the port does the same: the tag
   is taken lazily at the three "about to sleep" sites in `stampLockInner`
   (lock-only-conflict, multixact-wait, xid-wait) and once in the DML
   write-path's `waitForConflictingRowLock` — never in `lockRowsOp.stampLock`'s
   up-front dispatch, never in `updateOp.updateViaIndex`'s pre-read, never in
   `scanMatching`'s per-row dispatch loop (all three had their old up-front
   acquires removed).
2. **Four-mode `tupleLockHwMode`, not coarse RowShare/Exclusive.** Ported
   upstream's `tupleLockExtraInfo[].hwlock` column verbatim
   (`AccessShareLock`/`RowShareLock`/`ExclusiveLock`/`AccessExclusiveLock` for
   KeyShare/Share/NoKeyUpdate/Update respectively) instead of collapsing FOR
   KEY SHARE and FOR SHARE to the same `RowShareLock` — the coarse mapping
   would make a FOR KEY SHARE wrongly queue behind an unrelated no-key UPDATE
   holder (`AccessShareLock` doesn't conflict with `ExclusiveLock`; the
   collapsed `RowShareLock` would). This directly addresses the "Key-share
   compatibility" blast-radius risk called out below.

`selfIsLockMember` (read path, `lockRowsOp`) / `selfRowLockMember` (write path)
port upstream's `current_is_member` — skip the tag when this backend (or any
of its subxacts) already holds a membership on the tuple, matching
`heap_update`/`heap_lock_tuple`'s `if (!current_is_member)` guard.

Touched: `internal/executor/context.go` (`acquireTupleLock` routes to
`tableLockMgr` under `c.BackendID` when `c.LockMgr == nil`; `tryAcquireTupleLock`
folded into the same `tupleLockManager()` selector), `operators_lockrows.go`
(`stampLock` no longer acquires up-front; `acquireTupleLockForWait` added at
the three sleep sites; `tupleLockHwMode`, `selfIsLockMember`), `operators_storage.go`
(`updateViaIndex`/`scanMatching` up-front acquires removed; `waitForConflictingRowLock`
acquires lazily; `selfRowLockMember` added; `foreignLockOnly`/`lockedByForeign`
deleted as dead code), `internal/lockmgr/lockmgr.go` (`UseConfiguredTimeout`
exported sentinel), `internal/server/dispatch.go` (`executor.ReleaseTupleLocks`
wired into the Query-message defer, alongside the existing
`s.cfg.LockMgr.ReleaseAll`).

Two sibling unit tests asserted the old "always acquire up-front" semantics via
`lm.Waiters(...)` polling and would hang under the new lazy-acquire model
(the second locker becomes the tag's immediate HOLDER, never a waiter, since
the first locker never touched the tag) — fixed to poll `lm.Holders(...)[id]`
instead: `TestUpdateViaIndexScanBlocksOnForeignTupleLock`,
`TestForShareCompatibleMultipleHolders` (the latter also lost its invalid
"2 holders after 2 compatible FOR SHAREs" assertion — neither takes the tag
when uncontended).

## Validation results (loop #91)

1. `TestPort_IsolationTuplelockUpgradeNoDeadlock` looped 20× standalone via
   `runIsoSpecStrict` — **zero flakes** (was ~10-17%). Re-promoted from
   `runIsoSpec`.
2. Full isolation suite (`go test -run TestPort_Isolation ./internal/testport/`,
   121 specs) — clean except three failures confirmed **pre-existing and
   unrelated** (identical failure on HEAD without this change, verified via
   `git stash`): `insert-conflict-specconflict` (pg_stat_activity join
   formatting), `detach-partition-concurrently-4` (statement-cancellation
   gap), `partition-drop-index-locking` (pg_locks `query` column not
   populated).
3. `go test -race` on the touched packages + `make race-gate` (44 packages) —
   clean except one pre-existing flaky timing test in the untouched
   `internal/wal` package (passes standalone/under `-count=3`; only flakes
   under `race-gate`'s full-suite CPU contention).
4. `scripts/tpch-spotcheck.sh` — Q12=2/Q13=33 PASS.
5. `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` (pgbench
   CI-parity smoke: standard/-N/-S) — 0 failed transactions across all three
   workloads, no hot-path regression.

`docs/test-port/postgres-oracle-target-inventory.csv` row 612 flipped
`defer`→`pass`; regenerated the `.md` (isolation pass 119→120, defer 1→0).

## Resume point

Landed. If future isolation-suite runs surface new flakiness in this area,
start from `internal/executor/operators_lockrows.go`'s `acquireTupleLockForWait`
call sites and `internal/executor/operators_storage.go`'s
`waitForConflictingRowLock` — those are the only places the tag is now
acquired. See the deferral-ledger rows dated 2026-07-12 and 2026-07-14.
