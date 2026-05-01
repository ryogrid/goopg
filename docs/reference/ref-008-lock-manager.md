# REF-008: Lock Manager

## Overview

The lock manager provides relation-level and tuple-level locking
with deadlock detection. It is used by DML statements (INSERT,
UPDATE, DELETE, SELECT FOR UPDATE) to serialise concurrent access
to tables and rows.

## goopg Implementation

**Package:** `internal/lockmgr/`

### Key Types

- `LockManager` — central coordinator. Holds per-tag lock queues
  and a deadlock-detection graph.
- `LockTag` — identifies the resource being locked:
  `{DB, Rel, Block, Tuple}`.
- `Mode` — lock strength: `AccessShareLock`, `RowShareLock`,
  `RowExclusiveLock`, `ExclusiveLock`, etc.
- `BackendID` — per-connection identifier (monotonic `uint32`).
- `Waiter` — a backend blocked on a lock.

### Lock Acquisition

```
LockManager.Acquire(ctx, backendID, tag, mode)
  ├─ Lock lm.mu
  ├─ Walk the tag's lock queue
  │    └─ If no conflict: grant immediately, unlock, return
  ├─ If conflict:
  │    ├─ Register as waiter
  │    ├─ Build wait-for edge in deadlock graph
  │    ├─ Unlock lm.mu
  │    └─ Block on a per-backend condition variable (cond.Wait)
  └─ On wakeup: re-acquire lm.mu, check if lock granted
```

### Deadlock Detection

`CheckDeadlocksNow()` runs a cycle-detection pass over the
wait-for graph every time a new wait edge is added. If a cycle
is found, the youngest-backend victim is cancelled with
`ErrDeadlockDetected`. Upstream PostgreSQL probes the deadlock
graph on a timer and only when a lock wait exceeds
`deadlock_timeout`.

### Lock Release

`ReleaseAll(backendID)` removes all locks held by a backend and
wakes any waiters that can now be granted. Called at transaction
end.

## PostgreSQL Implementation (Deep Dive)

### Fast-Path Locking

PostgreSQL's lock manager has a **fast-path** optimisation for
relation-level `RowShareLock` and `RowExclusiveLock` (the most
common DML lock modes). Each backend has a small array
(`fastPathStrongRelationLocks`) where it records the locks it
holds without touching the shared lock hash table. Only when
another backend requests a conflicting lock on the same relation
does the system "promote" the fast-path entries to the shared
table.

This eliminates shared-memory contention for the vast majority
of lock acquisitions. In the pgbench workload, every UPDATE
acquires `RowExclusiveLock` on `pgbench_accounts`. With 4 clients,
all 4 would hold this lock simultaneously. With fast-path, no
shared-hash-table access is needed.

goopg does not have fast-path locking. Every call to
`acquireRelLock` goes through the global `LockManager`, which
acquires `lm.mu` and traverses the lock queue.

### Deadlock Timeout

PostgreSQL does not run the deadlock detector immediately on
every wait. Instead, it starts a timer (`deadlock_timeout`,
default 1 second). If the timer fires while the backend is still
waiting, the deadlock detector runs. This avoids the CPU cost
of cycle detection for short waits that are likely to resolve
quickly.

goopg runs `CheckDeadlocksNow()` (a full cycle-detection pass)
on every lock wait, regardless of the expected wait duration.
This adds unnecessary overhead for short-held locks.

### Lock Partitioning

PostgreSQL partitions the lock hash table into 16 partitions.
Each partition has its own LWLock, allowing concurrent access to
different partitions. This eliminates global lock-manager
contention under high concurrency.

goopg's `LockManager` uses a single `sync.Mutex` (`lm.mu`).
All lock operations queue behind this one mutex.

### Lock Tag Granularity

PostgreSQL supports multiple lock tag types:

| Tag Type | Scope | Example |
|----------|-------|---------|
| `LOCKTAG_RELATION` | Whole relation | `LOCK TABLE` |
| `LOCKTAG_PAGE` | Single page | Page-level locking |
| `LOCKTAG_TUPLE` | Single tuple | `SELECT FOR UPDATE` |
| `LOCKTAG_TRANSACTION` | Transaction ID | Waiting for another xact to commit |
| `LOCKTAG_OBJECT` | Database object | `LOCK DATABASE` |
| `LOCKTAG_USERLOCK` | Advisory lock | `pg_advisory_lock` |

goopg supports `LOCKTAG_RELATION` (via `acquireRelLock`) and
`LOCKTAG_TUPLE` (via `acquireTupleLock`).

### Lock Mode Conflict Matrix

PostgreSQL defines 8 lock modes with this conflict pattern:

```
              AccessShare RowShare  RowExclusive ...
AccessShare   ✓         ✓        ✓
RowShare      ✓         ✓        ✓
RowExclusive  ✓         ✓        ✗
ShareUpdate   ✓         ✓        ✗
Share         ✓         ✗        ✗
ShareRowExcl  ✓         ✗        ✗
Exclusive     ✗         ✗        ✗
AccessExcl    ✗         ✗        ✗
```

goopg supports `AccessShareLock`, `RowShareLock`,
`RowExclusiveLock`, and `ExclusiveLock`.

## goopg Improvement Analysis

### P0: Fast-Path Locking

Add a per-backend small-array fast path for `RowShareLock` and
`RowExclusiveLock` on relations. Only fall through to the shared
hash table when a conflict is detected.

**Impact:** Eliminates `lm.mu` contention for the most common
lock mode (RowExclusiveLock on DML tables). Estimated 5–10%
improvement for concurrent write workloads.

### P1: Deadlock Timeout

Replace immediate deadlock detection with a configurable timeout.
Start a timer on each lock wait; only run `CheckDeadlocksNow()`
when the timer expires.

**Impact:** Reduces CPU overhead for short lock waits (the common
case). The timer goroutine can walk the wait-for graph.

### P1: Lock Partitioning

Replace the single `sync.Mutex` with 16 partitions, each with
its own mutex. Hash the `LockTag` to select a partition.

**Impact:** Eliminates global lock manager contention under high
concurrency.

## References

- goopg: `internal/lockmgr/lockmgr.go`
- goopg: `internal/lockmgr/deadlock.go`
- PG lock manager: `postgres/src/backend/storage/lmgr/lock.c`
- PG fast-path: `postgres/src/backend/storage/lmgr/lmgr.c`
  (`FastPathGrantRelationLock`, `FastPathUnGrantRelationLock`)
- PG deadlock: `postgres/src/backend/storage/lmgr/deadlock.c`
- PG lockdefs: `postgres/src/include/storage/lockdefs.h`
