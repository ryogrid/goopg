# M0100-0005z — Non-HOT UPDATE t_ctid link + cross-page EPQ chain follower

Status: accepted (2026-05-15 loop 41)

## Context

`partition-key-update-4.spec` permutation 1 (`s1b s2b s2u1 s1u s2c s1c s1s`)
exercises READ-COMMITTED EvalPlanQual semantics across a partition key
UPDATE that waits on a concurrent in-place UPDATE:

* s2 commits `UPDATE foo SET b = b || ' update2' WHERE a = 1` — same
  partition (`foo1`), non-key column.
* s1's `UPDATE foo SET a = a + 1, b = b || ' update1' WHERE b like '%ABC%'`
  blocks on s2's xmax. After s2 commits, s1 must EPQ-refetch the updated
  tuple, re-evaluate WHERE (matches: `'ABC update2' LIKE '%ABC%'`), and
  re-evaluate SET against the refetched row to produce
  `(2, 'ABC update2 update1')`. The new `a=2` then routes the row from
  `foo1` to `foo2`.

Pre-fix s1 silently dropped its UPDATE. Final state was
`foo1 | 1 | ABC update2` (just s2's update); upstream produces
`foo2 | 2 | ABC update2 update1`.

## Root cause

The EPQ refetch path used `epqFollowHOT`, which delegates to
`followHOTChain`. That helper walks line-pointer chains gated by the
`HeapHotUpdated` infomask bit on each visited tuple.

goopg's non-HOT UPDATE path stamps `xmax` on the old tuple via
`PageSetHeapTupleXmax` — which leaves `HeapHotUpdated` clear and never
updates the old tuple's `t_ctid` to point at the successor — and then
inserts the new version through `writeHeapRowReturning`, which can land
the tuple on a different page (or, for a partition-key UPDATE, a
different relfile entirely).

Consequences:

* `followHOTChain` immediately terminates (no HOT bit, no on-page link).
* `epqFollowHOT` returns `chainFound=false`.
* The EPQ branch sets `epqSkipSeq = true` and silently discards the
  in-flight UPDATE.

## Fix

Two coupled additions in the storage and executor layers:

### 1. `internal/storage/heap.go` — `PageSetHeapTupleCtid`

New helper that overwrites only the `t_ctid` bytes (offsets 12–17 in the
tuple header) of an existing line-pointer-NORMAL tuple. Visibility
fields (`xmin`, `xmax`, `infomask`) are untouched. Caller holds the
page write lock. Companion to `PageSetHeapTupleXmax` and
`PageSetHeapTupleMovedPartition`; together they cover the three reasons
to mutate a tuple header in-place.

### 2. `internal/executor/operators_storage.go` — chain follower + stamp + wiring

* `epqFollowChain(ctx, rel, blk, slot, cols, pred)` walks the raw
  cross-page `t_ctid` chain (depth cap 64). Predicate evaluation happens
  only at the chain tail (the latest version) — matches PG's
  `heap_get_latest_tid` semantics. Returns `(newBlk, newSlot, row, true)`
  when the tail is visible to `ctx.Snap` AND satisfies `pred`, otherwise
  `(0, 0, nil, false)`. Sentinel-tagged tuples (moved-to-another-partition)
  terminate the walk so callers fall through to the existing
  `epqSlotMovedToAnotherPartition` raise path.
* `stampOldCtid(ctx, rel, blk, slot, newPtr)` pins+locks the old slot's
  page, calls `PageSetHeapTupleCtid`, swallows transient
  `ErrUnsupportedItem` / `ErrInvalidSlot` (best-effort link).

Wiring:

* SeqScan UPDATE path (`updateOp.Next`): after every
  non-cross-partition `writeHeapRowReturning`, call `stampOldCtid` so
  the old tuple's `t_ctid` points at the new `ItemPointer`. The
  cross-partition branch already stamps a sentinel via
  `PageSetHeapTupleMovedPartition` and must not be overwritten.
* IndexScan UPDATE path (`updateViaIndex`): same — promote
  `writeHeapRow` to `writeHeapRowReturning`, then `stampOldCtid`.
* SeqScan UPDATE EPQ branch and DELETE EPQ branch: after
  `epqFollowHOT` returns not-found, fall back to `epqFollowChain`
  before declaring the row skipped. Both branches now thread the new
  `(blk, slot)` through the retry (previously only `slot` was carried,
  which broke as soon as the chain crossed pages).

## Why this ordering is safe

* `stampOldCtid` runs AFTER `markHeapDeleteDirtyAndClearVM` and the new
  insert have already been logged; the additional update touches only
  the `t_ctid` bytes (no infomask change), so visibility decisions made
  by readers between the xmax stamp and the ctid stamp are unaffected.
* The new stamp is best-effort: a concurrent prune that flipped the old
  slot's line-pointer flags after the xmax stamp returns
  `ErrUnsupportedItem`, which `stampOldCtid` silently absorbs. EPQ
  followers in that case fall through to `epqSkip`, matching the
  pre-fix behavior for slots that lost their tuple body.
* `epqFollowChain`'s tail-only predicate evaluation matches PG: the
  middle versions are intentionally dead to our snapshot, and only the
  latest committed version is the candidate for re-locking.

## Verified regressions / scope

Pre-fix the new test `TestCrossPartitionUpdate_EPQReevaluatesSetAfterConcurrentInPlace`
fails with `final row = "xpu_foo1 1 ABC update2", want "xpu_foo2 2 ABC
update2 update1"`. Post-fix it passes.

Adjacent pins:

* `TestPageSetHeapTupleCtid` and `TestPageSetHeapTupleCtidInvalidSlot`
  in `internal/storage/heap_test.go`.
* `TestPort_IsolationPartitionKeyUpdate{1,2,3}` continue to pass.
* `TestPort_IsolationPartitionKeyUpdate4` permutation 1 (cross-partition
  UPDATE with concurrent non-trigger in-place UPDATE) is now byte-equal
  to upstream. Permutations 2 (`s2ut1` trigger) and 4 (`s2ut2` trigger)
  remain `defer` for an unrelated gap: cross-partition UPDATE in goopg
  fires only `before update` triggers on the source partition; upstream
  also fires `before delete` (cross-partition UPDATE = DELETE+INSERT
  internally). That follow-up is a separate sub-milestone — out of
  M0100-0005z scope.
* `go test -race ./internal/executor/ ./internal/storage/
  ./internal/server/ ./internal/mvcc/ ./internal/wal/ ./internal/planner/
  ./internal/parser/ ./internal/analyzer/ ./internal/access/btree/` PASS.
