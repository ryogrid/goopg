# 0012-0001 — Lock Manager Architecture (v0)

**Status:** accepted
**Milestone:** [0012 — Lock Manager and Deadlock Detection](../../milestones/0012-lock-manager-and-deadlock-detection.md)
**Spans seam:** lock tag model, mode-compatibility matrix, holder/waiter tracking
**Cross-links:**
[root-0001](../../root/root-0001-architecture-overview.md) (single-process Go arch),
[0002-0002](0002-0002-btree-concurrency.md) (per-page latches — separate
layer from this SQL-level lock manager).

## Context

goopg currently relies on local Go synchronization primitives at subsystem
boundaries (buffer slot latches, btree splitMu, mvcc.Manager mutex), but
exposes no PostgreSQL-style SQL lock manager. SQLSTATE `40P01`
(`deadlock_detected`) is defined in `internal/sqlstate/codes.go` but no
code path produces it.

This slice introduces the lock-manager *core surface*. It does not yet do
deadlock detection (M0012-0002) or wire executor/catalog paths into it
(M0012-0003). It establishes the data structures, mode semantics, and
acquire/release API that the next two slices will build on.

## Lock tag

For v0 the only lock tag granularity is **relation-level**:

```go
type LockTag struct {
    DB  uint32
    Rel uint32
}
```

The fork is intentionally excluded — index forks and the heap fork share
a single SQL-level lock, matching upstream's behaviour where a relation
lock covers all forks. Future granularities (page, tuple, transaction)
will introduce new tag families behind the same lock-table API.

## Lock modes

v0 implements **all eight upstream modes** by name and conflict shape so
the executor integration in M0012-0003 (and later milestones) can speak
the same vocabulary upstream uses. The full matrix lets us add modes
incrementally without revisiting the conflict table.

| Mode (numeric value)            | SQL mapping (v0+)                           |
|---------------------------------|---------------------------------------------|
| AccessShareLock (1)             | `SELECT`                                    |
| RowShareLock (2)                | `SELECT … FOR UPDATE / FOR SHARE`           |
| RowExclusiveLock (3)            | `INSERT`, `UPDATE`, `DELETE`                |
| ShareUpdateExclusiveLock (4)    | `VACUUM` (non-FULL), `ANALYZE`, `CREATE INDEX CONCURRENTLY` |
| ShareLock (5)                   | `CREATE INDEX` (non-CONCURRENTLY)           |
| ShareRowExclusiveLock (6)       | `CREATE TRIGGER`, some forms of ALTER       |
| ExclusiveLock (7)               | (rarely used by SQL — internal pieces)      |
| AccessExclusiveLock (8)         | `ALTER TABLE`, `DROP TABLE`, `VACUUM FULL`  |

The conflict table is taken verbatim from
`postgres/src/backend/storage/lmgr/lock.c`'s `LockConflicts[]` (lines
65-104). Same shape, same semantics, encoded as a `[9]uint16` bitmask
indexed by mode (slot 0 unused so `Mode-1` indexing matches upstream's
1-based numbering).

## Lock state

For each `LockTag` with at least one holder or waiter:

```go
type LockState struct {
    holders map[BackendID]LockMask // bitmask of modes held per backend
    waiters []*Waiter              // FIFO queue of pending requests
    granted LockMask               // union of all holders' masks
}
```

- `holders[b] & LOCKBIT(m) != 0` ⇔ backend `b` holds mode `m`.
- A backend may hold multiple modes on the same tag simultaneously
  (e.g. `RowExclusive` for INSERT plus `AccessShare` for an inner SELECT
  in the same xact). Each is a separate bit; the union is `granted`.
- `Waiter` carries `(Backend, Mode, signal chan)` so wakeup is a simple
  channel send; the waiter goroutine selects on it plus a context for
  cancellation.

## Acquire / Release semantics

```go
func (lm *LockManager) Acquire(ctx context.Context, b BackendID, t LockTag, m Mode) error
func (lm *LockManager) Release(b BackendID, t LockTag, m Mode)
func (lm *LockManager) ReleaseAll(b BackendID)
```

**Acquire algorithm:**

1. Take `lm.mu`.
2. If `t` has no `LockState`, install a fresh one, grant `m` to `b`, return.
3. If `b` already holds exactly `m`, it's a no-op — return.
4. If `canGrantImmediately(b, m)` is true, grant `m` to `b`, update
   `granted`, return. This covers two cases (see below): the plain
   no-waiter immediate grant, and the early grant ahead of a waiter.
5. Otherwise enqueue a `Waiter{b, m, signal}` at the tail.
6. Drop `lm.mu`. Block on `<-signal` (cancellable via `ctx`).
7. On wakeup, the waiter has been promoted to a holder by the releaser;
   return.

`TryAcquire` (the NOWAIT variant) is steps 1–4 verbatim, returning
`ErrLockNotAvailable` instead of enqueuing at step 5.

### Immediate-grant policy (`canGrantImmediately`)

A request for mode `m` by backend `b` is granted without blocking when
either upstream case holds:

1. **Plain immediate grant** — there are no queued waiters and `m` does
   not conflict with `granted_minus_self(b)`. The no-waiter precondition
   enforces strict head-of-line FIFO so a stream of
   compatible-with-current-holders newcomers can't starve a strong
   waiter (mirrors `LockAcquireExtended`'s grant in `lock.c`).
2. **Early grant ahead of a waiter** — `b` *already holds* a lock on `t`
   (`myHeldLocks != 0`) and would be inserted *ahead of* a waiter whose
   requested mode conflicts with what `b` already holds; and `m`
   conflicts with neither the modes of waiters ahead of that insertion
   point nor any other backend's current holdings. This is the "special
   case" early grant in `JoinWaitQueue` (`proc.c`): it lets a backend
   that already holds, say, `AccessExclusiveLock` take a weaker
   self-compatible mode immediately even while a conflicting
   `ExclusiveLock` waiter is parked ahead of it (the upstream
   `lock-nowait` isolation spec). The deadlock sub-case from upstream
   (the ahead waiter *holds* a lock conflicting with `m`) is subsumed by
   the `granted_minus_self` conflict check: that holder's mode is in the
   "others" mask, so the request simply declines and falls through to
   the normal wait path / deadlock detector. M0118-0003.

**Release algorithm:**

1. Take `lm.mu`.
2. Clear `LOCKBIT(m)` from `holders[b]`. If the resulting mask is 0,
   delete `holders[b]`.
3. Recompute `granted` as the OR of all remaining holder masks.
4. **Wake-pass:** walk `waiters` from head; for each, if its mode no
   longer conflicts with `granted_minus_self(b)`, promote it to a
   holder, remove from queue, send on its signal channel. Stop at the
   first non-promotable head (FIFO fairness — a stronger blocker
   shouldn't starve weaker waiters behind it, but a strict head-of-
   line policy is the simplest starvation-safe rule for v0).
5. Drop `lm.mu`.

`granted_minus_self(b)` is the OR of every holder's mask **except** `b`
— so a backend that already holds `RowExclusive` upgrading to
`ShareLock` doesn't conflict with itself.

`ReleaseAll(b)` is the per-backend cleanup hook the executor calls at
xact commit/rollback. It iterates every tag, releases every mode held by
`b`, and runs the wake-pass once per affected tag.

## Cancellation

If `ctx` cancels while waiting:

1. Take `lm.mu`.
2. Find the waiter struct in the queue, splice it out.
3. Drop `lm.mu`.
4. Return `ctx.Err()` (the executor maps this to a query-cancel error).

The wake-pass under Release uses non-blocking `select { case sig <- struct{}{}: default: }`
so a racing cancellation that already removed the waiter doesn't
deadlock the releaser. Net effect: no leaked waiter rows after a
cancellation, no leaked holder rows either (cancelled waits never
became holders).

## Concurrency model

A single coarse `lm.mu sync.Mutex` guards the whole table. Per-tag
striping is a future optimisation; for the v0 scope (single-process Go,
modest contention because the executor doesn't yet take many locks per
statement) one mutex is enough and lets the deadlock detector
(M0012-0002) walk the wait-for graph under one critical section without
second-order locking concerns.

The waiter's `signal` is an unbuffered chan. A naïve `sig <- struct{}{}`
under `lm.mu` would deadlock if the receiver wasn't yet parked — except
that the receiver's path is `lm.mu unlock → block on sig`, so the
releaser's send always finds a parked receiver. Still, the implementation
uses a buffered chan of size 1 to avoid relying on that ordering and to
keep cancellation lock-free.

## Deferred to follow-up slices

- **Wait-for graph + deadlock detection (M0012-0002):** uses
  `holders` + `waiters` directly to construct edges; no new data
  structures.
- **Executor / catalog integration (M0012-0003):** wires
  `Acquire(AccessExclusive)` into `DROP TABLE`,
  `Acquire(RowExclusive)` into INSERT/UPDATE/DELETE, etc. Pin/Unpin
  becomes `Acquire/Release` framed by `BeginTxn` / `EndTxn`.
- **Lock timeout:** the `lock_timeout` GUC isn't honoured in v0.
- **Predicate locking / SSI:** out of M0012 entirely.
- **`pg_locks` SQL view:** observability slice, follow-up.

## Testing

Unit tests in this slice exercise the core surface only:

- `TestLockManagerNoConflictGrantsImmediately`
- `TestLockManagerConflictBlocksAndWakesOnRelease`
- `TestLockManagerSelfDoesNotConflict` (same backend, AccessShare +
  RowExclusive in the same tag)
- `TestLockManagerMultipleHoldersOfCompatibleModes`
- `TestLockManagerWaiterCancellationCleansUp`
- `TestLockManagerReleaseAllWakesEveryone`
- `TestLockManagerFIFOFairness` (head waiter wakes before later
  waiters for the same conflicting mode)
- `TestLockConflictMatrixMatchesUpstream` (exhaustive 8x8 check
  against the table from postgres/src/backend/storage/lmgr/lock.c)
- `TestLockManagerEarlyGrantAheadOfWaiter` (M0118-0003 — backend
  already holding `AccessExclusiveLock` takes `ShareRowExclusiveLock`
  NOWAIT immediately while a conflicting `ExclusiveLock` waiter is
  parked; a third backend holding nothing still fails fast — pins the
  `JoinWaitQueue` early-grant special case)
- `TestParseModeRoundTrip` (M0118-0003 — `ParseMode` is the inverse of
  `Mode.String()` across all 8 modes and rejects unknown names; the SQL
  `LOCK TABLE` parser emits exactly these canonical names)
