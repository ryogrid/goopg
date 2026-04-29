# 0021-0008 — Tuple-Level Locking: Blocking Enforcement

**Status:** accepted (step 2b — UPDATE/DELETE block on foreign
lock-only xmax via lockmgr tuple tags; NOWAIT/SKIP LOCKED at
tuple level deferred)
**Milestone:** [0021 — Pessimistic Row Locking](../milestones/0021-pessimistic-lock-select-for-update.md)
(deferred follow-up: tuple-level locking on top of M0012).
**Spans seam:** lockmgr.LockTag, executor tuple-lock helpers,
lockRowsOp tuple-lock acquisition, scanMatching foreign-lock
detection.
**Cross-links:**
[0021-0005](0021-0005-tuple-level-locking-storage-and-mvcc.md)
(storage primitives + MVCC hook),
[0021-0006](0021-0006-tuple-locking-heap-lock-wal.md) (WAL
record),
[0021-0007](0021-0007-tuple-locking-producer-wiring.md)
(lockRowsOp xmax stamping).

## Context

Steps 1, 2a, and 3 produced storage primitives, the producer
wiring (lockRowsOp stamps lock-only xmax + emits WAL), and the
crash-recovery path. None of those slices made concurrent
UPDATE / DELETE actually **block** when another xact held a
row lock — the lock-only xmax was data on the page that
nobody yet enforced. This slice closes that gap: SELECT FOR
UPDATE registers a tuple-level holder in the lockmgr, and
UPDATE / DELETE detect the foreign lock-only xmax during their
scan and queue on the same lockmgr tag.

## lockmgr.LockTag extension

```go
type LockTag struct {
    DB     uint32
    Rel    uint32
    Block  uint32  // ← new (M0021 step 2b)
    Offset uint32  // ← new
}
```

Both Block/Offset zero is the historic relation-level tag (every
existing caller continues to work because Go zero-initialises
unset fields). Both non-zero — tuple-level tag. The relation
tag and the tuple tag are independent map keys, so
RowExclusiveLock at relation level doesn't accidentally
block tuple-level acquirers and vice-versa. Mirrors upstream's
separation between `LOCKTAG_RELATION` and `LOCKTAG_TUPLE`.

## Tuple-tag encoding

`tupleLockTag(rel, ItemPointer)` shifts Block/Offset by +1
when packing into the LockTag fields:

```go
func tupleLockTag(rel storage.RelFileNode, ptr storage.ItemPointer) lockmgr.LockTag {
    return lockmgr.LockTag{
        DB:     rel.DBOid,
        Rel:    rel.RelOid,
        Block:  uint32(ptr.Block) + 1,
        Offset: uint32(ptr.Offset) + 1,
    }
}
```

The +1 shift guarantees a real tuple at (block=0, slot=0)
doesn't alias the relation tag (block=0, offset=0). Slot 0 is
already invalid in the heap line-pointer convention so this is
just defence-in-depth.

## Executor helpers

```go
func (c *Context) acquireTupleLock(rel storage.RelFileNode, ptr storage.ItemPointer, mode lockmgr.Mode) error
func (c *Context) tryAcquireTupleLock(rel storage.RelFileNode, ptr storage.ItemPointer, mode lockmgr.Mode) error
```

Mirror `acquireRelLock` / `tryAcquireRelLock`'s SQLSTATE
mappings (40P01 deadlock, 57014 cancel, 55P03 NOWAIT lock
unavailable, XX000 fallback).

## SELECT FOR UPDATE — register the holder

`lockRowsOp.stampLock` now acquires `ExclusiveLock` on the
tuple tag _before_ the xmax stamp. The lockmgr records the
holder; transaction-scoped release via `LockMgr.ReleaseAll`
cleans up at commit/rollback (the dispatch.go hook from the
relation-lock lifecycle covers it without code changes).

## UPDATE / DELETE — block on foreign lock

`scanMatching`'s per-page collection now records `lockedBy` for
each match — the locker's xid when the tuple's xmax has
HEAP_XMAX_LOCK_ONLY set AND xmax != currentXID:

```go
func lockedByForeign(h, current) TransactionID {
    if foreignLockOnly(h, current) { return h.Xmax }
    return InvalidTransactionID
}
```

Captured at scan time so the dispatch loop doesn't have to
re-pin / re-RLock the page. After `s.RUnlock` and `Pool.Unpin`
release the page-level latch (necessary so the locker's
ReleaseAll can grab the page if needed at commit time), the
loop fires `fn` per match — but interposes an
`acquireTupleLock(rel, ptr, ExclusiveLock)` call when
`lockedBy` is set. The acquire blocks if the locker is still
alive; granted instantly if the locker has already
committed/aborted (ReleaseAll already cleared the holder).

After wake, `fn` proceeds with the regular
`PageSetHeapTupleXmax` path. The PageSetHeapTupleXmax helper
was extended to clear the HeapXmaxLockOnly + HeapXmaxLockMask
infomask bits on stamp, so the locker's tuple-lock metadata
doesn't leak into our delete/update bytes — without this fix,
mvcc.TupleVisible's lock-only short-circuit would mistake
our just-stamped xmax for a still-locked tuple and
erroneously keep the tuple visible.

## Tests

`internal/executor/operators_lockrows_test.go`:

- `TestUpdateBlocksOnForeignTupleLock` — NEW, multi-session
  test. Seed under a committed xact (so subsequent xacts see
  the rows live), session 1 holds backend ID 1 + own xact +
  runs SELECT id FROM items WHERE id=1 FOR UPDATE, session 2
  with backend ID 2 + own xact runs UPDATE on the same row in
  a goroutine, asserts session 2 registers as a waiter on the
  tuple tag (DB=1, Rel=905, Block=1, Offset=2 — the +1-shifted
  encoding for block 0 slot 1), releases session 1's holdings
  via LockMgr.ReleaseAll(1), confirms session 2 wakes and
  completes.
- All five pre-existing M0021 executor tests continue to pass.

Full `go test ./...` green; race-mode targeted runs across
executor / lockmgr / storage / wal / initdb / mvcc green
(post-flake confirmation, lockmgr's deadlock test was
transient).

## Out of scope

- IndexScan-driven UPDATE/DELETE blocking — needs IndexScan
  currentTID twin from step 2a IndexScan path (also
  deferred).
- Plumbing NOWAIT / SKIP LOCKED at the tuple level — the
  per-row dispatch loop currently always blocks; promoting
  to fail-fast / skip-locked needs threading the wait policy
  from the planner's UpdateStmt / DeleteStmt (which doesn't
  carry a wait policy — they always block in upstream too).
  For SELECT FOR UPDATE NOWAIT, the relation-level NOWAIT
  from step 0021-0004 already covers the relation tag; the
  tuple-tag NOWAIT path lives in lockRowsOp.stampLock and
  also stays deferred (would surface a 55P03 mid-stream
  rather than at Open).
- MultiXact-aware multi-holder support for FOR SHARE — the
  ExclusiveLock-only path doesn't allow multiple FOR SHARE
  holders on the same tuple. Upstream uses MultiXact ids;
  goopg deferred.
- Streaming per-row stamping — the two-pass buffer remains.
- pg_locks-style introspection of tuple-level holders.
- Lock-strength promotion / merge under multi-clause SELECT
  FOR UPDATE OF a NOWAIT FOR SHARE OF h.
