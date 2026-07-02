# 0118-0011 — UPDATE/DELETE MultiXact locker-preserving producer (M0118-0004 slice: tuplelock-upgrade-no-deadlock, perms 1–8)

Status: accepted (partial slice — spec stays `defer`; perm 9 still fails)

## Problem

The producer side of `tuplelock-upgrade-no-deadlock`. The read/consumer side
([[0118-0009]]) and the write-side EPQ-wait resolver ([[0118-0010]]) made every
*reader* of an updater-bearing MultiXact xmax correct, but goopg's UPDATE /
DELETE *writer* never **produced** such a multi. When a row already carried a
pre-existing non-conflicting lock-only xmax (e.g. a concurrent `FOR KEY SHARE`
holder) and another transaction performed a no-key UPDATE, goopg stamped the old
tuple's xmax with the updater's **single** TransactionID — silently dropping the
locker — instead of building a `{updater + surviving lockers}` MultiXactId the
way upstream `heap_update` / `heap_delete` do (`MultiXactIdCreate` /
`MultiXactIdExpand` on the `HEAP_XMAX_IS_LOCKED_ONLY` pre-existing-locker path).

Because the locker was lost, the spec's wait ordering came out wrong:
permutations 2/3 (s1 upgrades its key-share to a no-key UPDATE while s3 also holds
key share) mis-ordered the waiters, and the s3_update / s3_delete "can proceed"
permutations diverged from the expected isolation output.

## Change

Three pieces, all gated so the plain single-xid stamp stays the fast path:

1. **Storage primitive** `storage.PageStampHotOldTupleMulti` (internal/storage/
   heap.go) — the MultiXact-bearing sibling of `PageStampHotOldTuple`. Stamps the
   old-image tuple with a MultiXactId xmax + multi hint bits (clears
   `HEAP_XMAX_LOCK_ONLY`, sets `HEAP_XMAX_IS_MULTI`), refreshes
   `HEAP_KEYS_UPDATED` from the hint, writes the HOT chain CTID, sets
   `HEAP_HOT_UPDATED`, and advances `pd_prune_xid` from the **updater** xid (the
   multi id is not a TransactionID). Unit test `TestPageStampHotOldTupleMulti`.

2. **Producer helper** `stampUpdaterXmaxPreservingLockers(ctx, hdr, keysUpdated)`
   (internal/executor/operators_storage.go) — the UPDATE/DELETE twin of the
   row-lock path's `stampMultiLock` / `stampMultiUpdaterLock`. Gated on a
   pre-existing **lock-only** xmax; enumerates the existing holders (single or
   multi), keeps only still-active foreign lockers **whose lock mode does not
   conflict** with our update (`multixact.StatusesConflict`), appends our updater
   member (`StatusUpdate` for a key-changing UPDATE / DELETE, `StatusNoKeyUpdate`
   for a no-key UPDATE), and builds the multi via `CreateFromMembers` +
   `HintBits`. Returns `ok=false` (→ plain single-xid stamp) when there is no
   surviving non-conflicting foreign locker — the overwhelmingly common case.
   A thin non-HOT wrapper `stampUpdaterXmaxNonHOT` does the
   `PageSetHeapTupleXmaxMulti`-vs-`PageSetHeapTupleXmax` branch for the
   delete-half / DELETE sites. Unit test
   `TestStampUpdaterXmaxPreservingLockers`.

3. **Wiring (sibling-paths rule — every old-tuple xmax stamp updated together):**
   - `tryApplyHOTUpdate` HOT stamp (the spec path; keysUpdated=false).
   - index- and seqscan-driven UPDATE delete-half stamps (keysUpdated=true).
   - index- and seqscan-driven DELETE stamps and `DELETE ... USING`
     (keysUpdated=true).
   - `UPDATE ... FROM` delete-half stamp (keysUpdated=true).
   - `operators_merge.go` `mergeApplyUpdate` / `mergeApplyDelete`.
   - `operators_upsert.go` `applyUpdate` (keysUpdated from
     `onConflictUpdateTouchesKeyColumn`) and the upsert delete stamp.

The conflict filter makes a key-changing UPDATE / DELETE (`StatusUpdate`, which
conflicts with **every** lock mode incl. `FOR KEY SHARE`) a guaranteed no-op
here: nothing survives the filter, so those sites always plain-stamp. Only a
no-key UPDATE preserves a non-conflicting `FOR KEY SHARE` holder — the spec
scenario. The DELETE / key-UPDATE sites are wired anyway for sibling-path parity
and to centralise the logic in one helper.

## Crash safety

The HOT-update WAL record (`markHeapHotUpdateDirty` →
`EncodeHeapHotUpdate`) carries the **single** updater xid, not the multi id, and
`replayHeapHotUpdate` re-stamps the old slot with that single xid via
`PageStampHotOldTuple`. So on crash recovery the on-disk tuple degrades to the
single-xid updater stamp — which is **correct**: the transient lockers'
transactions do not survive the crash, and the update is preserved. This matches
the multixact-WAL-persistence deferral documented in [[0118-0002]] and the
precedent set by `stampMultiUpdaterLock` (which likewise marks the page dirty
with an in-memory multi the WAL cannot describe). The non-HOT delete-half /
DELETE records (`markHeapDeleteDirtyAndClearVM`, canonical insert/delete) carry
the single xid identically.

## Result

8 of the 9 `tuplelock-upgrade-no-deadlock` permutations now match the upstream
expected output (verified: the first divergence in `runIsoSpec` is at expected
line 216, inside permutation 9 which starts at line 194 — every prior permutation
matches). The previously-failing perms 2/3 (no-key UPDATE upgrade) and the
s3_update / s3_delete "can proceed" perms are fixed.

## Why partial / what's deferred

Permutation 9 (`s1_keyshare s3_for_update s2_for_keyshare s1_savept_e s1_share
s1_savept_f s1_fornokeyupd s2_fornokeyupd s0_begin s0_keyshare s1_rollback_f
s0_keyshare s1_rollback_e ...`) still diverges. It exercises the savepoint-driven
**tuple-lock retry** algorithm: s2 must re-run the whole tuple-lock acquisition
after initially avoiding a deadlock, and a `rollback to savepoint` must release a
subtransaction's lock so a blocked waiter becomes grantable. goopg has no
subtransaction-scoped row-lock release on `ROLLBACK TO SAVEPOINT`, so the spec
stays `defer`. Tracked in the deferral ledger; next slice.

Also still deferred (no behaviour change this slice): making the UPDATE / DELETE
**conflict-wait** multixact-aware so a *conflicting* lock-only locker is waited
for rather than dropped by the plain stamp (today an unwaited conflicting locker
is dropped — the pre-existing behaviour). **Update (2026-07-01, [[0119-0009]]):**
this was largely already true by the time it was written — `waitForConflictingRowLock`
(M0118-0003) covers `updateViaIndex`/`updateOp.Next`/`deleteOp.Next` — but it was
never wired at the sibling sites listed above (§3): `updateWithFrom`,
`deleteWithUsing`, `mergeApplyUpdate`/`mergeApplyDelete`, `upsertOp.applyUpdate`.
Closed in [[0119-0009]]; see that doc for the residual gaps (NND arbiter path,
`scanMatching`'s non-conflict-aware block).

## Verification

- `go build ./...`, `go vet ./internal/executor ./internal/storage` clean.
- New unit tests: `TestPageStampHotOldTupleMulti` (storage),
  `TestStampUpdaterXmaxPreservingLockers` (executor) — PASS.
- Race batch (`-race`): `Multixact*` / `Tuplelock*` / `LockUpdate*` /
  `UpdateLocked*` / `PropagateLock*` / `LockCommitted*` / `EvalPlanQual*` /
  `SkipLocked*` / `Nowait*` / `Merge*` / `StampUpdater*` / `*HOTUpdate*` /
  `Upsert*` — PASS. `internal/multixact`, `internal/storage` `-race` — PASS.
- Full `internal/executor`, `internal/wal`, `internal/mvcc` packages — PASS.
- CI-parity pgbench smoke (standard / -N / -S) — 0 failed transactions.
- `TestPort_IsolationTuplelockUpgradeNoDeadlock` — still SKIP (deferred): perms
  1–8 match, perm 9 diverges as described.

## Oracle

Upstream `src/backend/access/heap/heapam.c`: `heap_update` / `heap_delete` on the
`HEAP_XMAX_IS_LOCKED_ONLY` branch call `MultiXactIdCreate` / `MultiXactIdExpand`
(see `compute_new_xmax_infomask`) to fold the surviving non-conflicting lockers
into the new xmax alongside the updater. `DoesMultiXactIdConflict` /
`get_mxact_status_for_lock` drive the conflict filter mirrored by
`multixact.StatusesConflict`.
