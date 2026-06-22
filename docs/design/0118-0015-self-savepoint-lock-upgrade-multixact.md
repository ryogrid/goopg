# 0118-0015 — Self lock-upgrade inside a SAVEPOINT preserves the outer lock as a MultiXact member (M0118-0009 slice: delete-abort-savept-2)

**Status:** accepted
**Spec:** `postgres/src/test/isolation/specs/delete-abort-savept-2.spec`
**Test:** `TestPort_IsolationDeleteAbortSavept2` (`internal/testport/isolation_port_test.go`)
**Result:** all 4 permutations byte-identical vs PostgreSQL 18.3.

## Problem

`delete-abort-savept-2` is the "funkier" sibling of `delete-abort-savept`: instead
of a DELETE inside the savepoint it issues a pure **row-lock upgrade**. s1 holds
`FOR KEY SHARE` at the top level, opens `SAVEPOINT f`, takes
`FOR NO KEY UPDATE` (a stronger lock under the sub-XID), then `ROLLBACK TO f`. The
rollback drops only the sub-XID's member, so the row must revert to the outer
`FOR KEY SHARE`. A concurrent s2 probing with `FOR UPDATE` (which conflicts with
KEY SHARE) must therefore **keep waiting until s1 COMMITs**, while s2 probing with
`FOR NO KEY UPDATE` (compatible with KEY SHARE) may proceed.

goopg failed every permutation that lets s2's `FOR UPDATE` reach the row after the
rollback: s2 woke and completed immediately instead of waiting (expected
`<waiting ...>` then `<... completed>` only after `s1c`).

### Root cause

The row-lock writer (`lockRowsOp.stampLockInner`, `operators_lockrows.go`)
combined an existing lock-only xmax into a `MultiXactId` — preserving the prior
holder as a survivor — **only when the holder belonged to another transaction**
(`!o.isSelfXID(tup.Header.Xmax)`). When the *same* backend upgraded its own lock,
the code fell through to the single-xmax stamp
(`PageSetHeapTupleLockOnly(wxid, strength)`), overwriting the xmax with a single
member under the current writer XID.

For s1d that writer XID is the savepoint's sub-XID, so the stamp **discarded the
top-level KEY SHARE member**. `ROLLBACK TO f` then aborts the sub-XID, leaving the
row with no surviving lock at all — and s2's `FOR UPDATE` saw no conflict and
proceeded. The producer that preserves outer-level self members
(`stampMultiLock`, landed in [[0118-0012]] for `tuplelock-upgrade-no-deadlock`)
existed but was never reached on the self path.

This is the lock-only twin of the DELETE/UPDATE producer fix in [[0118-0013]]:
there the *updater* old-tuple stamp preserved the outer self locker; here the
*pure lock* upgrade stamp must do the same.

## Fix

`stampLockInner` now routes a **self** lock-only upgrade through `stampMultiLock`
when the existing xmax carries an active self member stamped under a level *other
than* the current writer XID — i.e. an outer savepoint / top-level lock that must
survive the inner upgrade.

- New predicate `lockRowsOp.hasOuterSelfLockMember(hdr)`: true iff a lock-only
  xmax (single or multi) holds an active member with
  `m.Xid != writerXID() && isSelfXID(m.Xid)`. A same-level self re-lock (the only
  member is our own current writer XID — e.g. a plain `FOR UPDATE` re-acquire)
  returns false.
- The MultiXact-producer gate is widened from `!isSelfXID(xmax)` to
  `!isSelfXID(xmax) || hasOuterSelfLockMember(hdr)`.

`stampMultiLock` already does exactly the right thing: it keeps every active
member whose xid differs from `writerXID()` as a survivor (exact-match on
`writerXID`, *not* `isSelfXID`, so the whole transaction tree is not collapsed)
and re-adds our current strength under the writer XID. For a same-level self
re-lock it finds no survivor and returns `combined=false`, so the caller falls
through to the unchanged single-holder stamp — the common, hot-path case is
untouched.

### Resulting behaviour (s1d builds `{top-level KEY SHARE, sub-XID NO KEY UPDATE}`)

- `s1r` (ROLLBACK TO f) aborts the sub-XID member; the top-level KEY SHARE
  member survives in the multi.
- s2 `FOR UPDATE`: `conflictingLockHolders` finds the still-active top-level
  KEY SHARE (conflicts with FOR UPDATE) and waits on it until `s1c` — the aborted
  sub-XID member is filtered by `IsXIDActive`. ✓ (perms 1, 2)
- s2 `FOR NO KEY UPDATE`: KEY SHARE does not conflict, so s2 combines into the
  multi and proceeds immediately after the rollback. ✓ (perms 3, 4)

## Crash safety / scope

Lock-only MultiXact membership is process-shared in-memory state and is not
persisted through the single-xid heap-lock WAL record (same caveat as
`stampMultiLock`/`stampMultiUpdaterLock`); the holders' transactions do not
survive a crash, so losing the membership on recovery is correct. MultiXact WAL
persistence remains deferred ([[0118-0002]]).

The change is a strict no-op outside a savepoint (`writerXID() == ctx.Tx.XID`, so
no member can be an outer self member) and for foreign holders (the existing
`!isSelfXID` arm is unchanged).

## Oracle

Mirrors `heap_lock_tuple`'s `MultiXactIdExpand`/`MultiXactIdCreate` on the
`HEAP_XMAX_IS_LOCKED_ONLY` branch
(`postgres/src/backend/access/heap/heapam.c`), where a self lock-strength upgrade
across sub-transactions records each level as a distinct multixact member so
`ROLLBACK TO SAVEPOINT` reverts to the surviving outer member.

## Verification

- `TestPort_IsolationDeleteAbortSavept2` — 4/4 permutations PASS.
- No regression: `TestPort_Isolation{DeleteAbortSavept,AbortedKeyrevoke,
  TuplelockUpgradeNoDeadlock,LockUpdateDelete,SkipLocked*,Nowait*}` PASS.
- `-race` `internal/mvcc` + `internal/multixact` PASS; `internal/executor` +
  `internal/storage` unit PASS.
- CI-parity pgbench smoke (TPC-B + `-N` + `-S`) 0 failed.

## Deferred (ledger 2026-06-22)

`multixact-no-forget` (whole-txn ROLLBACK of an updater member must retain the
surviving locker on the read path) and the remaining M0118-0009 misc specs.
