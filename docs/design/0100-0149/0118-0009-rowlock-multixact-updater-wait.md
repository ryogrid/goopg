# 0118-0009 — Row-lock read path: MultiXact-aware updater wait (M0118-0004 slice: tuplelock-upgrade-no-deadlock, partial)

Status: accepted (partial slice — spec not promoted)

## Problem

The `tuplelock-upgrade-no-deadlock` isolation spec has two remaining failing
permutations after the chain-tail crash fix ([[0118-0008]]):

```
# perm 2 / perm 3 — wait-queue upgrade priority
s1_keyshare s2_for_update s3_keyshare s1_update s3_update s1_rollback s3_rollback s2_rollback
```

Expected PostgreSQL order after `s1_rollback`: the existing key-share holder
`s3` upgrading its **own** lock (`s3_update`) completes **first**, and only after
`s3_rollback` does the pure waiter `s2_for_update` complete. goopg completed
`s2_for_update` first.

### Root cause (two halves)

1. **Read side (this slice).** `lockRowsOp.stampLockInner`'s real-updater branch
   captured `xmax := tup.Header.Xmax` and fed that raw value directly to
   `TxnMgr.IsXIDActive` / `WaitForXID` / `HasAbortedXID`. When the tuple's `xmax`
   is a **MultiXactId** (`HEAP_XMAX_IS_MULTI`) — e.g. `{s1 no-key-update
   (updater), s3 key-share}` formed by `stampMultiUpdaterLock` — the raw multi
   value is **not** a `TransactionID`; those single-xid APIs misinterpret it.
   A FOR UPDATE waiter therefore ignored a surviving co-holder (`s3`'s key-share)
   recorded alongside the updater and proceeded out of order.

2. **Producer side (deferred — NOT in this slice).** goopg's UPDATE path stamps
   the old tuple's `xmax` with a single updater xid
   (`PageSetHeapTupleXmax(page, slot, Tx.XID)` at
   `operators_storage.go:2638` and `:3279`, plus the merge/upsert twins). It does
   **not** preserve a pre-existing non-conflicting lock-only locker into a new
   `{updater + survivors}` MultiXactId the way upstream `heap_update` →
   `compute_new_xmax_infomask` / `MultiXactIdCreate` does. So in perm 2,
   `s1_update` discards `s3`'s key-share membership entirely; even a correct read
   side then has nothing to wait on.

## Change (read side)

In `stampLockInner`'s real-updater branch, before releasing the page:

- When `IsHeapTupleXmaxMulti(infomask)`, resolve the real updater via
  `updaterXID(header)` and use **that** for the abort/commit/chain decision
  (replacing the raw multi value).
- Collect every **other** still-active member (via `activeLockHolders`, which
  already skips `Tx.XID`) whose lock we must also wait on, and wait on them
  first — honouring the `SKIP LOCKED` / `NOWAIT` / blocking wait policy exactly
  as the updater wait does. After the wait, re-evaluate the tuple from scratch
  (`stampLockInner(rel, ptr, depth+1)`), mirroring `heap_lock_tuple`'s
  `MultiXactIdWait` → `goto l3` retry.

An existing holder upgrading its own lock is excluded from the wait set
(`activeLockHolders` filters `Tx.XID`), so it proceeds ahead of a pure waiter —
matching PostgreSQL's release order **once the producer preserves the member**.

This corrects a real latent bug (raw MultiXactId fed to single-xid txn APIs) on
every multixact-with-updater read path — `lock-update-traversal`,
`lock-update-delete`, `propagate-lock-delete`, etc. — independently of the spec.

## Why partial / what's deferred

The spec is **not** promoted. perms 2/3 still fail because the producer-side
MultiXact preservation (root cause #2) is unimplemented, and perm 9 needs the
savepoint-driven lock-retry (`heap_lock_tuple` `HeapTupleUpdated` re-run after
`rollback to savepoint` changes multixact membership). Both are tracked in the
deferral ledger.

Producer-side plan (next slice): add a shared
`stampUpdaterXmaxPreservingLockers(ctx, rel, blk, slot)` helper that, **only when
the old tuple already carries a foreign non-conflicting active lock-only xmax**
(single or multi), forms a `{Tx.XID as updater + surviving non-conflicting
members}` MultiXactId via `PageSetHeapTupleXmaxMulti` instead of the plain
single-xid stamp; the common pgbench case (no foreign locker) keeps the plain
stamp, bounding blast radius. The UPDATE conflict-wait must then be made
multixact-aware in both directions (wait on a conflicting member; let an existing
member upgrade). Sites: `operators_storage.go:2638`, `:3279`, and the
`operators_merge.go` / `operators_upsert.go` twins (sibling-paths rule).

## Verification

- `go build ./...`, `go vet ./internal/executor`, gofmt clean on changed lines.
- Multixact / row-lock regression batch PASS (no behaviour change on currently
  passing specs): `LockUpdateDelete`, `LockUpdateTraversal`, `UpdateLockedTuple`,
  `TuplelockConflict`, `TuplelockUpdate`, `TuplelockPartition`,
  `PropagateLockDelete`, `MultixactNoDeadlock`, `SkipLocked*`, `Nowait*`,
  `LockCommittedUpdate/Keyupdate`, `Merge*`, `EvalPlanQual*`.
- `tuplelock-upgrade-no-deadlock` remains deferred (perms 2/3/9 unchanged this
  slice — they require the producer-side work above).

## Oracle

Upstream `src/backend/access/heap/heapam.c` — `heap_lock_tuple`
(`MultiXactIdWait` on a multixact xmax before deciding) and `heap_update`
(`compute_new_xmax_infomask` / `MultiXactIdCreate` preserving non-conflicting
remaining members into the updated tuple's `xmax`).
