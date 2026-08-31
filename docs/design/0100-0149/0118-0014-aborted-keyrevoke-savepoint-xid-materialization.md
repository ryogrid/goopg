# 0118-0014 — Eager top-level XID materialization at SAVEPOINT (aborted-keyrevoke)

Status: accepted
Milestone: M0118-0009 (isolation spec port — misc/system-level)
Spec: `postgres/src/test/isolation/specs/aborted-keyrevoke.spec`
Test: `TestPort_IsolationAbortedKeyrevoke` (`internal/testport/isolation_port_test.go`)

## Summary

`aborted-keyrevoke` now PASSES all 14 permutations byte-identical vs PostgreSQL
18.3. The spec opens a SAVEPOINT, does a key-changing `UPDATE foo SET key = 2`
(obtaining "KEY REVOKE", i.e. `HEAP_KEYS_UPDATED`), `ROLLBACK TO` the savepoint
(losing the key revoke), then takes `FOR KEY SHARE`; a second session also takes
`FOR KEY SHARE`. The key behaviours are:

- while the savepoint's key-UPDATE is in progress, a concurrent `FOR KEY SHARE`
  must **wait** on it (perms 7–9), then read the original row after the rollback;
- a rolled-back savepoint's NEW tuple version must be invisible to everyone;
- after `ROLLBACK TO`, both sessions' `FOR KEY SHARE` are compatible and proceed.

## Root cause — `SAVEPOINT` before first write registered a zero parent link

Transaction XIDs are assigned **lazily** (only on first write/lock). The spec
issues `BEGIN; SAVEPOINT f;` *before* any write, so at `execSavepoint` time the
session's top-level `tx.XID` was still `InvalidTransactionID` (0).
`execSavepoint` called `AllocateSubXid(sess.tx.XID)` → `RegisterSubXid(subxid, 0)`,
recording the sub-XID with **parent = 0** in the pg_subtrans-backed `SubxactMap`.

A zero parent link is poison for cross-session resolution. When the second
backend later inspects the savepoint's tuples:

- `TopLevelXid(subxid)` returns 0 (parent chain dead-ends at 0), so the reader's
  snapshot cannot map the sub-XID to a *running* top-level xact and wrongly
  classifies the savepoint's uncommitted NEW tuple (`xmin = subxid`) as
  **visible** — s2's `FOR KEY SHARE` returned the phantom `key = 2` row instead
  of waiting;
- `xidActiveWithSubxact(subxid)` is false (its top resolves to 0, not in
  progress), so the row-lock wait path in `lockRowsOp.stampLockInner` never
  blocked on the in-progress updater.

`delete-abort-savept` (0118-0013) escaped this only because its s1 takes
`FOR KEY SHARE` **before** the savepoint, which assigns the top-level XID first,
so the subsequent `AllocateSubXid` saw a non-zero parent.

## Fix

`execSavepoint` (`internal/executor/operators_tx.go`) now materialises the
top-level XID **before** allocating the sub-XID, when the session has no XID yet:

```go
if sess.tx.XID == storage.InvalidTransactionID {
    if err := o.ctx.MaterializeWriterXID(); err != nil {
        return err
    }
}
subXid, err := o.ctx.TxnMgr.AllocateSubXid(sess.tx.XID)
```

`MaterializeWriterXID` assigns the top-level XID via `AssignXID`, sets
`ctx.Tx.XID`, and calls `BasicSession.OnTopLevelXIDAssigned` to sync `sess.tx.XID`.
The sub-XID's parent link is therefore non-zero from birth. It is a strict no-op
once a top-level XID already exists (prior write, lock, or an outer savepoint —
the first savepoint materialises the XID, so nested savepoints inherit it).
Eager assignment at SAVEPOINT only consumes an XID slightly earlier; it does not
change any spec output (these specs do not observe `txid_current`).

## Supporting write-side / visibility pieces (same loop)

These were prerequisites for the rolled-back NEW tuple to disappear and for the
in-progress key-UPDATE to be recognised as a key conflict:

1. **Sub-XID-scoped NEW-tuple xmin.** `writeHeapRowReturning`,
   `writeHeapRowReturningPG`, and the HOT path in `tryApplyHOTUpdate` now stamp
   `xmin = effectiveWriterXID(ctx)` (the current sub-XID inside a savepoint)
   instead of the top-level `ctx.Tx.XID`. A row inserted in a savepoint — including
   an UPDATE's new version — then flips invisible when the savepoint's sub-XID is
   marked aborted (`MarkSubxactAborted`), via the subxact-aware visibility check.
   Twins of the old-tuple xmax stamp from 0118-0013; strict no-op outside a
   savepoint.

2. **`HEAP_KEYS_UPDATED` on the single-xid old-tuple stamp.**
   `stampUpdaterXmaxNonHOT` now sets `HEAP_KEYS_UPDATED` on the old tuple when the
   UPDATE/DELETE changes a key, on the single-xid fallback (the common case where
   there is no pre-existing locker to fold into a multixact). The multixact path
   already encoded this through the updater member's `StatusUpdate` hint bits.
   Without it a concurrent `FOR KEY SHARE` read `keysUpdated = false` and skipped
   the conflict/wait. Mirrors upstream `heap_update`/`heap_delete`.

3. **Descendant-direction subxact self-visibility** in `isCurrentTxXID`
   (`internal/mvcc/subxact_visibility.go`): a still-open sub-XID of our own
   transaction tree (a savepoint we opened) is treated as self for visibility —
   tuples it wrote are self-visible — while an individually rolled-back sub-XID is
   excluded so its writes fall through to the abort/snapshot check and become
   invisible. The pre-existing code resolved only the ancestor hop (sub-XID →
   top-level); the live sub-XID writer is the reverse direction.

## Oracle

Mirrors upstream `heap_update`/`heap_delete` (`HEAP_KEYS_UPDATED` on the old
tuple) and lazy XID assignment (`GetCurrentTransactionId` /
`AssignTransactionId` in `src/backend/access/transam/xact.c`): a subtransaction's
parent is its immediate parent in the xact stack, never an unassigned 0.
Behaviour compared against `./postgres/local_install` PG 18.3 via the spec's
expected output.

## Verification

- `go build ./...` clean.
- `-race`: `internal/mvcc`, `internal/multixact` PASS.
- `internal/executor`, `internal/storage` unit suites PASS.
- Isolation specs: `TestPort_IsolationAbortedKeyrevoke` PASS (14/14);
  siblings `delete-abort-savept`, `tuplelock-upgrade-no-deadlock`,
  `lock-update-delete` still PASS (no regression). `delete-abort-savept-2` and
  `multixact-no-forget` remain deferred (separate fix_plan items, unchanged).
- CI-parity pgbench smoke: 0 failed.

## Deferred

- `delete-abort-savept-2` — FOR NO KEY UPDATE pure-lock upgrade restore on the
  row-lock path.
- `multixact-no-forget` — whole-txn ROLLBACK of an updater member must retain the
  surviving locker.

Related: [[0118-0012]] (subxact-scoped row-lock release), [[0118-0013]]
(subxact-scoped DELETE/UPDATE xmax for savepoint rollback).
