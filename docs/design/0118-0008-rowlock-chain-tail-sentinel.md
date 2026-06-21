# 0118-0008 — Row-lock chain-follow: committed-DELETE chain-tail sentinel (M0118-0004 slice: tuplelock-upgrade-no-deadlock, partial)

Status: accepted

## Problem

The `tuplelock-upgrade-no-deadlock` isolation spec exercises many permutations of
multiple sessions taking row-level locks of varying strength (FOR KEY SHARE / FOR
SHARE / FOR NO KEY UPDATE / FOR UPDATE / UPDATE / DELETE) on a single row. One
permutation crashes goopg:

```
s1_keyshare s2_for_update s3_keyshare s3_delete s1_rollback s3_commit s2_rollback
```

1. `s1` takes `FOR KEY SHARE` (lock-only `xmax`).
2. `s2` requests `FOR UPDATE` — conflicts, **waits** (`<waiting ...>`).
3. `s3` takes `FOR KEY SHARE` — joins the SHARE multixact.
4. `s3` `DELETE`s the row — waits behind `s1`'s key share.
5. `s1` rolls back; `s3`'s `DELETE` completes; `s3` commits.
6. `s2`'s `FOR UPDATE` wakes and re-evaluates the now committed-deleted row.

Expected (PG 18.3): `s2` sees `(0 rows)` — the row is gone.

goopg instead raised `ERROR: short read at block`.

### Root cause

When `s2` wakes, it re-enters `lockRowsOp.stampLockInner`. The tuple now carries a
**committed real `xmax`** (the `s3` DELETE), so it takes the committed-updater
branch and follows the `t_ctid` chain to find the live successor under READ
COMMITTED. The "no live successor" guard only checked for a **self-pointing**
CTID:

```go
if next.Block == ptr.Block && next.Offset == ptr.Offset { ... } // deleted
```

But a goopg DELETE does **not** rewrite `t_ctid` (`PageSetHeapTupleXmax` only
stamps `xmax`/infomask). A never-updated row keeps its initial CTID of
`{InvalidBlockNumber, 0}` (only an UPDATE rewrites it via `stampOldCtid`). That
sentinel is **not** self-pointing, so the guard fell through, the code Pinned
block `InvalidBlockNumber` (0xFFFFFFFF), and `relFile.readBlock` returned
`ErrShortRead` (block ≥ nblocks).

The sibling EPQ walk `epqFollowChainFull` already handled this correctly with a
broader `atTail` test — the row-lock path had drifted out of sync with it.

## Change

`internal/executor/operators_storage.go` — extract the chain-tail test the EPQ
walk already used into a shared helper:

```go
func isChainTailCTID(ctid storage.ItemPointer, curBlk storage.BlockNumber, curSlot uint16) bool {
    return ctid.Block == storage.InvalidBlockNumber || ctid.Offset == 0 ||
        (ctid.Block == curBlk && ctid.Offset == curSlot)
}
```

- `epqFollowChainFull` now calls `isChainTailCTID` (was inline) — behavior
  identical, just deduplicated.
- `lockRowsOp.stampLockInner` (`internal/executor/operators_lockrows.go`) replaces
  its self-only check with `isChainTailCTID`, so a committed DELETE of a
  never-updated row is recognized as a chain tail instead of being followed into a
  non-existent block.
- The deleted-row return is changed to signal **`epqSkipped = true`** so the
  caller (`drainAndStamp`) **drops** the row rather than yielding the stale
  pre-delete version — matching PG's EvalPlanQual returning no tuple. (The old
  self-CTID return used `epqSkipped = false`, but that path was effectively dead
  for goopg, whose initial CTID is `{InvalidBlockNumber, 0}` and never
  self-pointing, so the change is behavior-preserving for existing paths.)

This keeps the two sibling chain-follow paths (EPQ recheck and row-lock
re-evaluation) terminating identically, per the project's sibling-paths rule.

## Scope

This slice fixes the committed-DELETE re-fetch crash only. The
`tuplelock-upgrade-no-deadlock` spec is **not yet promoted** — two failures
remain and are deferred as deeper multixact-with-updater wait-ordering work:

- **Wait-queue upgrade priority** (perms 2, 3): after `s1` rolls back, the
  existing key-share holder `s3` upgrading its own lock should proceed **before**
  the pure waiter `s2`. goopg currently wakes `s2` first. The committed-updater
  branch in `stampLockInner` (line ~719) does not yet special-case a multixact
  `xmax` that carries both an updater and other lockers (`stampMultiUpdaterLock`
  territory), so the wake/conflict re-evaluation order diverges.
- **Savepoint-driven lock retry** (perm 9): `s1_fornokeyupd` deadlocks / times out
  where PG re-runs the overall tuple-lock algorithm after `rollback to savepoint`
  changes the multixact membership. Needs the heap_lock_tuple
  HeapTupleUpdated-style retry.

`deadlock-parallel` remains deferred (no lock-group abstraction).

## Verification

- The crashing permutation now returns `(0 rows)`; `ERROR: short read at block`
  no longer appears in the spec output.
- New unit test `TestIsChainTailCTID` (`internal/executor/chain_tail_ctid_test.go`)
  guards the sentinel logic in CI (the spec test stays `t.Skip` deferred).
- Regression (all PASS): `TestPort_Isolation` `LockUpdateDelete`,
  `LockUpdateTraversal`, `UpdateLockedTuple`, `TuplelockConflict`,
  `TuplelockUpdate`, `TuplelockPartition`, `PropagateLockDelete`,
  `MultixactNoDeadlock`, `SkipLocked{,2,3,4}`, `Nowait{,2,3,4,5}`,
  `LockCommitted{Update,Keyupdate}`, `MergeUpdate`, `MergeDelete`,
  `MergeMatchRecheck`, `EvalPlanQual{,Trigger}`.
- `go build ./...`, `go vet ./internal/executor/` clean; `go test
  ./internal/executor/` PASS; `-race` on key row-lock isolation tests green.
- gofmt: changed lines clean (pre-existing go1.25/go1.26.3 alignment noise in
  unrelated regions left untouched — see memory `goopg_gofmt_version_mismatch_no_w`).

## Oracle

- Spec: `postgres/src/test/isolation/specs/tuplelock-upgrade-no-deadlock.spec`
- Expected: `postgres/src/test/isolation/expected/tuplelock-upgrade-no-deadlock.out`
- Mirrored logic: `src/backend/access/heap/heapam.c` (`heap_lock_tuple` /
  `heap_fetch` chain-follow terminating on an invalid/self `t_ctid`; a DELETE
  leaves `t_ctid` pointing at itself upstream, the equivalent of goopg's
  `{InvalidBlockNumber, 0}` initial sentinel).
