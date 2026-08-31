# 0118-0010 — UPDATE/DELETE/MERGE EvalPlanQual wait: MultiXact-aware updater resolution (M0118-0004 slice: tuplelock-upgrade-no-deadlock, partial)

Status: accepted (partial slice — spec not promoted)

## Problem

The write-side twin of the read-side bug fixed in [[0118-0009]]. When an
UPDATE / DELETE / MERGE finds a tuple that `isConcurrentlyUpdated` flags as
modified by another transaction, the EvalPlanQual (EPQ) retry loop captured the
raw `xmax` and fed it straight to `epqWait` (→ `WaitForXID`, the wait-for graph)
and, at two sites, to `snap.HasInProgress` / `TxnMgr.HasAbortedXID` /
`TxnMgr.IsXIDActive`:

```go
xmax := oldTup.Header.Xmax   // raw t_xmax
...
if epqWait(ctx, xmax) { ... }
```

When the tuple's `xmax` is an **updater-bearing MultiXactId**
(`HEAP_XMAX_IS_MULTI` set, `HEAP_XMAX_LOCK_ONLY` clear) the raw `t_xmax` is a
MultiXactId, **not** a TransactionID. `isConcurrentlyUpdated` itself was already
made multixact-aware ([[0118-0009]] / M0118-0003): it resolves the updater
member before its abort/self checks. But the EPQ-wait code that runs *after* it
returns true still used the raw value — so the waiter registered a wait-for-graph
edge on, and blocked on, a bogus transaction id (the multixact id read as a
TransactionID). This is a latent correctness bug independent of the spec:
`stampMultiUpdaterLock` (the FOR KEY SHARE-joins-an-updater producer, M0118-0003)
can place an updater-bearing multi on a tuple that a later UPDATE / DELETE then
EPQ-waits on.

## Change

Add one shared resolver in `operators_storage.go`:

```go
func concurrentModifierXID(hdr storage.HeapTupleHeader, mxs *multixact.Store) storage.TransactionID {
    if storage.IsHeapTupleXmaxMulti(hdr.Infomask) && !storage.IsHeapTupleLockOnly(hdr.Infomask) {
        if upd := multixactUpdaterXID(mxs, hdr.Xmax); upd != storage.InvalidTransactionID {
            return upd
        }
    }
    return hdr.Xmax
}
```

It returns the real updater member for an updater-bearing multi (mirroring
`isConcurrentlyUpdated`'s own resolution), the raw xmax for a single-xid xmax,
and falls back to the raw xmax only when the multi is unresolvable (store nil /
membership lost after a restart) — matching the conservative path
`isConcurrentlyUpdated` took to return `true`.

Every EPQ-wait site now derives `xmax` through it instead of reading
`Header.Xmax` directly. Because the value drives not only `epqWait` but also the
`HasInProgress` / `HasAbortedXID` / `IsXIDActive` re-checks at the
`updateOpIndex` / `deleteOpIndex` sites, resolving once at the assignment keeps
all uses consistent.

Sites changed (sibling-paths rule — every EPQ-wait twin updated together):

- `operators_storage.go`: HOT-update race check (`tryApplyHOTUpdate`); the
  index-driven and seqscan-driven UPDATE delete+insert EPQ loops (both the
  initial conflict and the post-write re-check); the index/seqscan DELETE EPQ
  loops; and the `UPDATE ... FROM` EPQ loop. (7 sites.)
- `operators_merge.go`: `mergeApplyUpdate` and `mergeApplyDelete` EPQ waits.
  (2 sites.)

Behaviour is unchanged for the overwhelmingly common single-xid xmax (the
resolver returns the raw value); it only differs when the xmax is an
updater-bearing multixact, which never arises on the pgbench TPC-B / TPC-H hot
path.

## Why partial / what's deferred

This is a prerequisite, not the promotion. `tuplelock-upgrade-no-deadlock`
remains `defer`: perms 2/3 still mis-order `s2_for_update` vs `s3_update`, and
perm 9 (`s1_fornokeyupd`) still times out. Both require the **producer side** —
goopg's UPDATE path must preserve a pre-existing non-conflicting locker into a
`{updater + survivors}` MultiXactId instead of the plain single-xid stamp.

Producer-side discovery this slice (refines [[0118-0009]]'s plan): the spec's
`update tlu_job set name = …` does **not** change an index key, so it is
**HOT-eligible** — the old-tuple stamp goes through
`storage.PageStampHotOldTuple` inside `tryApplyHOTUpdate`, **not** the
`PageSetHeapTupleXmax` sites at `operators_storage.go:2638`/`:3279`. The producer
therefore needs a new storage primitive that stamps a MultiXact xmax while still
writing the HOT chain CTID and `HEAP_HOT_UPDATED` flag (a multi-aware variant of
`PageStampHotOldTuple`), in addition to wiring the plain-stamp delete+insert /
merge / upsert sites. That primitive + full UPDATE-hot-path gates
(pgbench smoke + regress-port) are the next slice. Tracked in the deferral
ledger.

## Verification

- `go build ./...`, `go vet ./internal/executor` clean.
- Unit batch (with `-race`): `Multixact*`, `Tuplelock*`, `LockUpdate*`,
  `UpdateLocked*`, `PropagateLock*`, `LockCommitted*`, `EvalPlanQual*`,
  `SkipLocked*`, `Nowait*`, `Merge*` — PASS, no regression.
- `internal/multixact` `-race` — PASS.
- `TestPort_IsolationMultixactNoDeadlock` PASS (promoted spec stays green);
  `TestPort_IsolationTuplelockUpgradeNoDeadlock` still deferred (skips on
  mismatch — perms 2/3/9 unchanged, as expected without the producer).

## Oracle

Upstream `src/backend/access/heap/heapam.c`:
`HeapTupleHeaderGetUpdateXid` (resolve the update member of a multixact xmax
before waiting), invoked by `heap_update` / `heap_delete` on the
`TM_BeingModified` path before `XactLockTableWait` / `MultiXactIdWait`.
