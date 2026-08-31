# 0118-0063 — `detach-partition-concurrently-4`: cursor-pinned snapshot at DECLARE + abort-releases-snapshot (M0118-0008, partial)

**Status:** accepted (partial — detach-4 still NOT promoted; only the
`WHERE CURRENT OF` positioned-update permutations remain)

## Context

`detach-partition-concurrently-4` exercises foreign-key behaviour in the face of
a concurrent `ALTER TABLE … DETACH PARTITION … CONCURRENTLY`. After the FK
existence-check fix (design [0118-0062](0118-0062-detach-partition-concurrently-4-fk-current-epoch.md))
the residual divergence was confined to:

1. the **cursor permutations** (spec lines 61–73): a cursor over the partitioned
   parent declared before the detach must (a) still see the declaration-time
   partition set at FETCH and (b) make the detacher *wait*; and
2. an **abort-ordering** divergence in a non-cursor FK permutation (spec line 57,
   `s1brr s1s s2detach s1insert s1c`): PG reports `s2detach: <... completed>`
   **before** `s1c`, goopg reported it after.

This design closes both classes except the three `WHERE CURRENT OF` permutations,
which need positioned UPDATE/DELETE (a distinct feature, deferred).

## Problem & root causes

### A. Lazy cursor materialisation (goopg) vs eager portal open (PG)

PostgreSQL opens a cursor's portal at `DECLARE`: it plans the query, takes the
cursor's snapshot, and acquires `AccessShareLock` on the referenced relations.
goopg materialised a cursor **lazily on first FETCH** (`cursorEntry`,
`internal/server/conn_tx.go`). Consequences for the spec:

- **Snapshot:** a cursor declared before the detach but fetched after saw the
  *post-detach* partition set (1 row) instead of the declaration-time set
  (2 rows), because the FETCH-time scan used a fresh per-statement snapshot
  whose `PartitionDetachEpoch` already reflected the detach.
- **Locking:** `DECLARE` took no lock, so the concurrent `DETACH … CONCURRENTLY`
  did not wait — it completed immediately rather than rendering `<waiting ...>`.

### B. Abort does not release the RR/SSI pinned snapshot

For the line-57 permutation the detacher waits on s1's REPEATABLE READ pinned
snapshot via `WaitForPinnedSnapshotsToCommit`. goopg's wait keyed off `inTxn`,
which only clears at the explicit `COMMIT`/`ROLLBACK` (`s1c`). PostgreSQL's
`AbortTransaction` drops the transaction snapshot the **moment** a top-level
statement errors (here `s1insert`'s FK violation), so the detacher unblocks
before `s1c`. goopg kept the snapshot pinned until `s1c`, inverting the reported
completion order.

### C. Cancel during the pinned-snapshot wait reported the wrong message

`WaitForPinnedSnapshotsToCommit` → `WaitForSlotsToCommit` returns the raw
`context` error on cancellation; the detach call site returned it verbatim, so a
`pg_cancel_backend` during that wait surfaced `ERROR: context canceled` instead
of PG's `ERROR: canceling statement due to user request`. (The
`waitForRelationLockers` path already mapped this correctly; the pinned-snapshot
path did not.)

## Changes

### 1. Eager cursor materialisation at DECLARE (`internal/server/dispatch.go`)

The `DeclareCursorStmt` branch now calls `materializeCursor` immediately after
`cursorDeclare`, inside the explicit transaction. Because the materialising scan
runs through the normal seq/index opens, it takes a **txn-scoped `AccessShare`**
on every relation it reads (`acquireScanReadLockTxn`, held to commit) — so the
concurrent detacher parks behind the open cursor via `waitForRelationLockers` —
and it buffers the **declaration-time snapshot's** rows, so a later FETCH returns
the pre-detach partition set. goopg already buffered all rows at first FETCH, so
this only shifts the materialisation point earlier (no new memory cost) and is
strictly more PG-faithful (a cursor's snapshot is fixed at open). A
materialisation error now surfaces at DECLARE, as in PG (planning/opening happen
at DECLARE). `executeFetch` already guards on `!cur.Materialized`, so FETCH reads
the buffer without re-running the query.

### 2. Abort releases the pinned snapshot (`internal/mvcc/manager.go`, `internal/server/conn_tx.go`, `internal/server/dispatch.go`)

- New `mvcc.Manager.ReleasePinnedSnapshot(handle)` clears the slot's `pinnedSnap`
  marker **without** ending the transaction (`inTxn` stays 1 until the eventual
  ROLLBACK) and broadcasts `commitCond` to wake waiters.
- New `mvcc.Manager.WaitForPinnedSnapshotsReleased` (replacing the
  `WaitForSlotsToCommit` call inside `WaitForPinnedSnapshotsToCommit`) waits until
  each captured slot has `inTxn==0` **or** `pinnedSnap==false` — i.e. the snapshot
  is gone, whether released by commit (`End`) or by abort
  (`ReleasePinnedSnapshot`).
- `connTxState.ReleasePinnedSnapshotOnFail(mgr)` calls it after `Fail()`, gated on
  `SavepointDepth()==0` exactly like the abort lock-release (design 0118-0032):
  when a savepoint is open the error aborts only the subtransaction and
  `ROLLBACK TO SAVEPOINT` resumes the **same** RR snapshot, which must be
  retained. Wired at both `Fail()` call sites in `dispatchSimpleQueryViaExecutor`.

### 3. Cancel-message mapping in the detach pinned-snapshot wait (`internal/executor/operators_ddl.go`)

The `WaitForPinnedSnapshotsToCommit` result is now mapped through
`lockWaitCancelError` (the same helper `waitForRelationLockers` uses), so a cancel
during that wait reports `canceling statement due to user request`
(SQLSTATE 57014) and a timeout reports the matching lock/statement-timeout
message.

## Result

Probe (`RunAndCompare`): the first divergence moved from spec line 80 (the
FK/abort permutation) to the `WHERE CURRENT OF` permutations (spec lines 71–73)
— every fetchall cursor permutation, every cancel/abort permutation, the
independent-session and `VACUUM FREEZE pg_inherits` permutations now byte-match
PG 18.3.

## Deferred — `WHERE CURRENT OF` positioned UPDATE (the only remaining blocker)

`s1updcur` (`update d4_fk set a = 1 where current of f`) is parsed
(`AlterTableStmt`/`UpdateStmt.CurrentOf`) but **not executed** — no server/
executor site consumes `CurrentOf`. Implementing it requires: capturing each
buffered cursor row's CTID at materialisation (the slot carries
`ctidBlock`/`ctidOff`/`hasCTID`, but the cursor only clones `Row=[]Datum`),
tracking the current position's CTID, and restricting the UPDATE/DELETE to that
CTID. That is a distinct positioned-DML feature; tracked in the deferral ledger
for a dedicated loop. Until it lands, detach-4 stays `defer`.

## Gates

- Probe: only the 3 `WHERE CURRENT OF` permutations remain divergent.
- Regression (strict, no change): `TestPort_IsolationDetachPartitionConcurrently1/2/3`,
  `…VacuumNoCleanupLock` (cursor, pass-required), `…AlterTable1/2/3`,
  `…TruncateConflict`, `…VacuumConflict`, `…ClusterConflict`, `…FkContention`,
  `…FkSnapshot`, `…ReferentialIntegrity`, `…SimpleWriteSkew`, `…ReadOnlyAnomaly`,
  `…InheritTemp`, `…DeleteAbortSavept{,2}`, `…AbortedKeyrevoke`,
  `…SubxidOverflow` all PASS.
- Units: `go test ./internal/mvcc/... ./internal/server/... ./internal/executor/...`
  PASS; `go test -race ./internal/mvcc/...` PASS.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Oracle

PostgreSQL `src/backend/commands/portalcmds.c` (`PerformCursorOpen` opens the
portal and takes the snapshot/locks at DECLARE), `src/backend/access/transam/xact.c`
(`AbortTransaction` releases the transaction snapshot at abort),
`src/test/isolation/specs/detach-partition-concurrently-4.spec`.
