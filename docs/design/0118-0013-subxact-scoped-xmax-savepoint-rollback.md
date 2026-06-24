# 0118-0013 — Subtransaction-scoped DELETE/UPDATE xmax for savepoint-rollback visibility

Status: accepted
Milestone: M0118-0009 (Misc / system-level isolation specs)
Spec landed: `delete-abort-savept` (PASS, all 7 permutations vs PG 18.3)

## Problem

`delete-abort-savept.spec` exercises the upstream invariant:

> After rolling back a subtransaction that upgraded a lock, the previously held
> lock should still be held.

```
s1: BEGIN; SELECT * FROM foo FOR KEY SHARE;   -- outer KEY SHARE lock
s1: SAVEPOINT f;
s1: DELETE FROM foo;                          -- lock upgrade under the subxact
s1: ROLLBACK TO f;                            -- undo the DELETE; restore KEY SHARE
s1: COMMIT;
s2: SELECT * FROM foo FOR UPDATE;             -- must see (1,1) and (in the
                                              --   pre-commit permutations) wait
                                              --   on the restored KEY SHARE lock
```

goopg returned `(0 rows)` for `s2`: the row stayed logically deleted even though
the DELETE's subtransaction was rolled back.

## Root cause

goopg already had the savepoint machinery — `SAVEPOINT` allocates a sub-XID
(`AllocateSubXid` → `RegisterSubXid`, writing the attached pg_subtrans
`SubxactMap`), `session.currentSubXid` tracks the live sub-XID across Query
messages, and `ROLLBACK TO` calls `MarkSubxactAborted`. The visibility helper
`TupleVisibleSubxact`/`SeesCommittedXIDWithSubxacts` already consults
`IsAborted`. But three pieces did not use the sub-XID:

1. **Write side.** Every heap DELETE/UPDATE stamped the old tuple's xmax (and its
   WAL record) with `ctx.Tx.XID` — the *top-level* XID. The per-statement
   `ectx.Tx` is rebuilt from the connection's top-level transaction each Query
   (`dispatch.go: ectx.Tx = tx`), so it never reflects an open savepoint. With
   xmax = the top-level XID (which commits), the deletion was indistinguishable
   from a committed one even though its subxact had rolled back.

2. **Producer.** The DELETE was a *lock upgrade* over a row this same xact already
   held `FOR KEY SHARE` (stamped at the outer level). The multixact producer
   `stampUpdaterXmaxPreservingLockers` dropped every member from our own
   transaction tree, so the outer KEY SHARE locker was clobbered by the plain
   single-xid stamp — nothing survived for `ROLLBACK TO` to restore.

3. **Row-lock reader.** `lockRowsOp.stampLockInner` translated "xmax settled" into
   commit-vs-abort via `HasAbortedXID`, which only knows the top-level aborted
   set (`Rollback`), not a `ROLLBACK TO SAVEPOINT` recorded in pg_subtrans. So a
   rolled-back subxact deletion was treated as committed: the code followed the
   (chain-tail) CTID to "no live successor" and dropped a row still present.

## Fix

Mirrors upstream `heap_delete`/`heap_update` stamping `GetCurrentTransactionId()`
(the *current subtransaction* id), and `heapam.c`'s lock-upgrade handling.

### 1. `effectiveWriterXID(ctx)` (operators_storage.go)

New free helper, the storage-op twin of `lockRowsOp.writerXID`: returns
`session.EffectiveWriterXID()` (the current sub-XID inside a savepoint) else
`ctx.Tx.XID`. **Strict no-op outside a savepoint** — `EffectiveWriterXID()`
returns the top-level XID there.

Applied at **all 24 old-tuple-xmax-determining sites** (sibling-paths rule —
page stamp and its paired WAL record must agree): the HOT update old-tuple stamp
(`PageStampHotOldTuple`/`Multi`) + `markHeapHotUpdateDirty`; the index/seqscan
UPDATE delete-half (`PageSetHeapTupleMovedPartition`/`PageSetHeapTupleXmax` +
`markHeapDeleteDirtyAndClearVM`); the DELETE path; `UPDATE…FROM` and
`DELETE…USING`; MERGE update/delete; upsert update/delete; and the plain-stamp
fallback inside `stampUpdaterXmaxNonHOT`. Also the updater member appended by
`stampUpdaterXmaxPreservingLockers`.

The DELETE/HOT WAL records still carry the single (sub-)XID, so crash recovery
degrades to a single-xid stamp; the whole top-level xact is aborted on crash, so
the row reverts regardless (multixact WAL persistence deferred per [[0118-0002]]).

### 2. Producer preserves outer-level self lockers (operators_storage.go)

`stampUpdaterXmaxPreservingLockers` now keeps a member from our own transaction
tree **at an outer level** (`TopLevelXid(m.Xid) == ourTop` but `m.Xid != ourXID`)
as a survivor, bypassing the foreign-locker liveness/conflict filtering (self
never conflicts with self). A DELETE inside a savepoint over a self-`FOR KEY
SHARE`-locked row therefore builds `{outer-keyshare + subxid-updater}`; dropping
the rolled-back subxid member leaves the KEY SHARE alive. Only the **exact**
current writer sub-XID is dropped (re-added as the updater). Outside a savepoint
there are no other same-tree members, so behaviour is unchanged.

### 3. Row-lock reader treats a subxact-aborted xmax as rolled back (operators_lockrows.go)

`stampLockInner`'s "updater rolled back" test becomes
`HasAbortedXID(xmax) || IsAborted(xmax)`: the first arm is the whole-transaction
aborted set, the second is the pg_subtrans subxact map. A subxact-aborted xmax is
now correctly handled as a rolled-back updater (row live at the original ptr,
stamp our lock) instead of a committed delete. `IsAborted` returns false for a
non-subxact xid, so the top-level path is unchanged.

## Behaviour after the fix

- **Post-commit permutations** (e.g. `s1l s1svp s1d s1r s1c s2l`): xmax = the
  `{outer-keyshare, subxid}` multi; the subxid is aborted and the outer KEY SHARE
  committed, so `s2`'s FOR UPDATE locks the live row and returns `(1,1)`.
- **Pre-commit permutations** (e.g. `s1l s1svp s1d s1r s2l s1c`): after the
  rollback the multi still carries the **active** outer KEY SHARE member, so
  `lockRowsOp` waits on it (`<waiting ...>`); when `s1` commits, `s2` re-probes,
  finds the updater subxid aborted, and locks the row → `(1,1)`.

## Tests / gates

- `TestPort_IsolationDeleteAbortSavept` — PASS, all 7 permutations byte-identical
  vs `./postgres/local_install` PG 18.3.
- Regression (no engine path left behind): the full M0118-0003 row-lock +
  M0118-0004 deadlock batch (`LockUpdate*`, `Tuplelock*`, `Nowait*`,
  `SkipLocked*`, `LockCommitted*`, `UpdateLockedTuple`, `PropagateLockDelete`,
  `LockNowait`, `MultixactNoDeadlock`, `TuplelockUpgradeNoDeadlock`) + MERGE /
  insert-conflict specs — all PASS.
- `-race`: `internal/mvcc`, `internal/multixact`, `internal/executor` PASS;
  `internal/storage`, `internal/wal` PASS.
- CI-parity pgbench smoke (pre-commit hook) — 0 failed.

## Still deferred (ledger)

- `delete-abort-savept-2` — the subxact upgrade is `FOR NO KEY UPDATE` (a pure
  lock, not a delete); needs the row-lock path to preserve/restore an
  outer-level self lock-only member across `ROLLBACK TO` the same way the
  producer now does for the updater path.
- `aborted-keyrevoke` — the rolled-back step is an `UPDATE` whose **new** tuple's
  xmin must also be stamped under the sub-XID (so the rolled-back successor
  version becomes invisible); this design only addresses the old-tuple xmax.
- `multixact-no-forget` — a whole-transaction `ROLLBACK` (not a savepoint) of an
  updater that formed a `{locker, updater}` multi; the surviving locker member
  must be retained while the aborted updater is forgotten on the read path.

Related: [[0118-0011]] (the producer this builds on), [[0118-0012]] (subxact-scoped
row-lock release), [[0118-0002]] (multixact WAL persistence, deferred).
