# 0118-0004 — Simple (lock-upgrade) deadlock: synchronous early detection

Status: accepted
Milestone: M0118-0003 (isolation row-locking pass-through), slice 16
Related: [0012-0002 deadlock detection algorithm](0012-0002-deadlock-detection-algorithm.md),
[0118-0003 transaction-scoped LOCK TABLE](0118-0003-txn-scoped-lock-table.md)

## Problem

The upstream isolation spec `deadlock-simple.spec` exercises the deadlock
detector's special case for **simple** deadlocks — a lock *upgrade* attempted
while another process is already waiting for a lock upgrade on the same object,
where the sought locks conflict with those already held so neither upgrade can
complete:

```
session s1: BEGIN; LOCK a1 ACCESS SHARE; LOCK a1 ACCESS EXCLUSIVE  -- waits (s2 holds AS)
session s2: BEGIN; LOCK a1 ACCESS SHARE; LOCK a1 ACCESS EXCLUSIVE  -- closes the cycle
```

Expected PostgreSQL output:

```
step s1ae: LOCK TABLE a1 IN ACCESS EXCLUSIVE MODE; <waiting ...>
step s2ae: LOCK TABLE a1 IN ACCESS EXCLUSIVE MODE;
ERROR:  deadlock detected
step s1ae: <... completed>
```

The decisive detail is that **s2ae errors immediately, with no `<waiting ...>`
marker**. `pg_isolation_tester` only prints `<waiting ...>` when its 10 ms poll
of `pg_isolation_test_session_is_blocked()` observes the backend parked on a
lock. PostgreSQL never parks s2 here: `JoinWaitQueue` (proc.c) detects the
cycle the instant s2 tries to join the queue and returns
`PROC_WAIT_STATUS_ERROR` synchronously — **without waiting for
`deadlock_timeout`** (default 1 s).

### goopg before this slice

goopg's `tableLockMgr` (from slice 15) correctly routed the LOCK TABLE upgrades
through `lockmgr`, but `lockmgr` only had the **timeout-driven** detector
(`time.AfterFunc(deadlockTimeout, runDeadlockCheck)`). So goopg parked *both*
upgraders, the runner observed both as `<waiting ...>`, and the deadlock error
surfaced late (after the survivor had already committed). Actual goopg output
diverged on victim **timing** and the spurious `<waiting>` marker — even though
victim *selection* (youngest backend) was already correct.

## Mechanism mirrored

`JoinWaitQueue` performs this check before sleeping (proc.c): when the requester
already holds locks on the object (`myHeldLocks != 0`) and there are queued
waiters, it scans the queue. For the first waiter `w` whose requested mode
conflicts with a lock the requester already holds ("`w` must wait for me"), it
decides:

1. **early deadlock** — if the requester's new mode *also* conflicts with a lock
   `w` already holds ("I must wait for `w`"): `RememberSimpleDeadLock`,
   `early_deadlock = true`, return `PROC_WAIT_STATUS_ERROR`. The requester is the
   victim.
2. **early grant** — else if the new mode conflicts neither with the requests
   ahead of the insertion point nor with other holders: grant immediately (this
   is the `lock-nowait` special case, already implemented in
   `lockState.canGrantImmediately`).
3. **ordinary wait** — else insert ahead of `w` and sleep.

Only the first "must wait for me" waiter decides the outcome.

## Implementation

`internal/lockmgr/lockmgr.go`:

- New `lockState.hasSimpleDeadlock(b, m)` mirrors the JoinWaitQueue scan and
  reports case 1. It returns at the first waiter `w` (≠ `b`) whose `w.Mode`
  conflicts with `b`'s held mask; the result is `ConflictsWith(m, holders[w])`
  — i.e. the requester must also wait for `w`. Returns false when `b` holds
  nothing here or the queue is empty (no simple-deadlock cycle is possible).
- `Acquire` calls it after `canGrantImmediately` (mutually exclusive at the
  deciding waiter) and before enqueueing. On a hit it unlocks, calls
  `ReleaseAll(b)`, and returns `ErrDeadlockDetected`.

The `ReleaseAll(b)` is load-bearing: the victim's transaction is aborted by the
deadlock error, and in PostgreSQL the abort releases all of its locks — which is
exactly what lets the surviving upgrader (`s1ae`) proceed. Mirroring the
existing timeout-victim path (`<-w.victim` → `ReleaseAll(b)`) drops s2's
lingering `ACCESS SHARE` promptly and runs the wake-pass that promotes s1ae.
The executor's eventual `ReleaseTableLocks` at ROLLBACK/COMMIT is then an
idempotent no-op.

`ErrDeadlockDetected` is mapped to SQLSTATE `40P01` by
`executor.Context.acquireRelLockTxn` (unchanged from slice 15).

## Scope / non-goals

This is the **simple** (immediate, single-object lock-upgrade) deadlock only.
General multi-object cycles, soft-deadlock queue reordering, and the row-lock
wait graph (`deadlock-hard`, `deadlock-soft*`, `tuplelock-upgrade-no-deadlock`,
`multixact-no-deadlock`) remain on the timeout-driven detector / future slices —
the row-lock path waits via `xmax`/`WaitForXID`, not `lockmgr`, so its waits are
still invisible to this detector. `TryAcquire` (NOWAIT) is left on its existing
`ErrLockNotAvailable` path; upstream would prefer the deadlock error there, but
no current target spec exercises NOWAIT-with-simple-deadlock.

## Verification

- `TestPort_IsolationDeadlockSimple` — byte-for-byte match against
  `expected/deadlock-simple.out` (promoted `failed` → `pass` in the inventory
  CSV; coverage doc pass count 52 → 53).
- `go test -race ./internal/lockmgr/...` — includes the linear-waiter-chain
  false-positive guard (no spurious `ErrDeadlockDetected`).
- Sibling LOCK TABLE / row-lock specs unaffected: `TestPort_IsolationLockNowait`
  (early-grant special case), `TestPort_IsolationTuplelockConflict`,
  `TestPort_IsolationLockCommittedKeyupdate` all still PASS.
