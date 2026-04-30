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

## PostgreSQL Implementation

PostgreSQL's lock manager (`lock.c`) is significantly more complex:

- **Lock Methods** — each lock method defines a set of lock modes
  and conflict rules. The default method (`DEFAULT_LOCKMETHOD`)
  supports 8 lock modes (AccessShare through AccessExclusive).
- **Lock hash table** — a shared hash table keyed by `LOCKTAG`.
  Each entry holds a queue of `PGPROC` backends waiting for the
  lock.
- **Fast-path locking** — for simple locks (RowShareLock on a
  relation), PostgreSQL uses an unlogged per-backend array
  (`fastPathStrongRelationLocks`) to avoid touching the shared
  hash table at all. This eliminates shared-memory contention for
  the common case.
- **Deadlock timeout** — unlike goopg's immediate detection,
  PostgreSQL waits `deadlock_timeout` (default 1s) before
  running the cycle detector. This trades a short wait for the
  cost of the detection pass.
- **Tuple-level locking** — tracked via `xmax` / `HEAP_XMAX_*`
  infomask bits on the tuple itself, not in the lock manager.
  goopg uses `acquireTupleLock` which delegates to the same
  `LockManager`.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Lock table | `sync.Map` of `LockTag` → `*LockState` | Shared hash table in DSM |
| Deadlock detection | Immediate on every wait | Timeout-based (deadlock_timeout) |
| Fast path for simple locks | Not implemented | Per-backend fast-path array |
| Tuple locking | Lock manager (`acquireTupleLock`) | xmax / HEAP_XMAX_* infomask bits |
| Per-backend condition variable | `sync.Cond` per BackendID per wait | `PGSemaphore` per PGPROC |

## Potential Optimisations or Corrections

- **Fast-path locking** for relation-level locks (RowShareLock /
  RowExclusiveLock) would eliminate hash-table contention for the
  common DML case. This is the single highest-impact lock-manager
  optimisation available.
- **Deadlock timeout** (instead of immediate detection) would
  avoid the cycle-detection overhead for short waits that resolve
  quickly.

## References

- goopg: `internal/lockmgr/lockmgr.go`
- goopg: `internal/lockmgr/deadlock.go`
- PG lock manager: `postgres/src/backend/storage/lmgr/lock.c`
- PG deadlock: `postgres/src/backend/storage/lmgr/deadlock.c`
- PG fast-path: `postgres/src/backend/storage/lmgr/lmgr.c`
  (`GrantLockLocal`, `FastPathGrantRelationLock`)
