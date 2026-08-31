# lockRowsOp: committed-update CTID chain follow + RR/SER 40001

## Problem

`TestPort_IsolationLockCommittedKeyupdate` tests `SELECT … FOR KEY SHARE` when
another session has committed an UPDATE that changes the primary key column.

### Failing cases (before fix)

**Permutations 1–2 (RC)**: `s2l` blocks at the advisory lock while `s1` updates
`id=3→id=2` and commits.  After s1ul releases the advisory lock, s2l resumes.
SeqScan's snapshot has `s1.xid` as `InProgress` (snapshot taken before s1c), so
it emits the old tuple `(id=3, xmax=s1.xid)` as visible.  `lockRowsOp.stampLock`
saw the real non-lock-only xmax from another xact (M0100-0005f guard) and skipped
stamping — returning the old row value `3|two` instead of `2|two`.

**Permutation 3 (RC)**: `s2l` starts after s1ul but before s1c.  Without a
lockmgr exclusive lock registered by the UPDATE (goopg UPDATE never calls
`acquireTupleLock`), `stampLock`'s `acquireTupleLock` returned immediately.  The
in-progress xmax was also skipped (M0100-0005f guard), so s2l completed without
blocking (no `<waiting …>`) and returned the stale row.

**RR permutations**: s2l should raise `40001 could not serialize access due to
concurrent update` when a committed update is detected.  Old code skipped stamping
silently; no error was raised.

## Fix

### 1. `stampLock` → `(ItemPointer, bool, error)` with chain-following

`stampLock` now returns `(successorPtr, followed, err)`.  When the M0100-0005f
guard fires (real non-lock-only xmax from another xact):

1. **Wait**: if `TxnMgr.IsXIDActive(xmax)` → call `WaitForXID`.  This blocks
   the goroutine (producing `<waiting …>` in isolation tests) until the updater
   commits or aborts.
2. **Aborted**: re-stamp at the original ptr via `stampAtPtr` (row is live).
3. **Committed under RR/SER**: return `40001 could not serialize access due to
   concurrent update` — mirrors upstream `heap_lock_tuple` for non-RC isolation.
4. **Committed under RC**: follow the CTID chain to the live successor via
   `stampLockInner` (depth-limited to 16 hops), acquire a lockmgr lock on the
   successor, stamp it, and return `(successorPtr, followed=true, nil)`.

### 2. `drainAndStamp` records chain-follow result

When `stampLock` returns `followed=true`, `drainAndStamp` sets
`entry.newPtr = successorPtr` and `entry.newPtrValid = true` on the pending row.

### 3. `lockRowsOp.Next()` refetches the row

When `entry.newPtrValid`, `refetchRow(rel, newPtr)` re-reads the successor tuple
from the heap and decodes it with `DecodeRowIntoMctxPGTuple`.  This returns the
updated row values (`id=2, value=two`) instead of the stale row (`id=3, value=two`).

## Results

- `TestPort_IsolationLockCommittedKeyupdate`: **PASS** (all 12 permutations).
- PASS count: 10 → **11** (adds LockCommittedKeyupdate).
- Previously passing tests: LockCommittedUpdate, InsertConflictDoUpdate,
  InsertConflictDoNothing, FkSnapshot, PartitionKeyUpdate{1,2,3,4},
  InsertConflictDoUpdate4, ReadWriteUnique — all unchanged.

## Limitations / follow-up

- `acquireTupleLock` for the successor slot is acquired but the original ptr's
  lockmgr lock is not released.  Both locks are held until COMMIT; for v0 this
  is conservative but harmless.
- HOT-updated tuples whose old version's CTID still points to self (no
  `stampOldCtid` call): chain follow terminates at the self-pointer check and
  returns nothing.  The non-HOT path (M0100-0005z) is the only case where
  stampOldCtid is called, so HOT updates with key-column changes are out of
  scope (HOT is already ineligible when indexed columns change).
