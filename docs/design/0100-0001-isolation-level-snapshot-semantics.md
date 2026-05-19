# 0100-0001 — Isolation-Level Snapshot Semantics (RR/Serializable BEGIN-time snapshot)

**Status:** accepted
**Date:** 2026-05-13
**Milestone:** M0100-0001
**Closes:** M0096-0013 (one of the documented remaining blockers)

## Problem

PostgreSQL's READ COMMITTED takes a fresh snapshot at the start of every
statement; REPEATABLE READ and SERIALIZABLE take a snapshot at the first
statement of the transaction and hold it for the duration of the
transaction.

goopg's MVCC layer already implements the correct logic in
`internal/mvcc/manager.go:197-224` (`SnapshotFor`):

- For `ReadCommitted` (line 208–213): always call `captureSnapshotLocked()`.
- For `RepeatableRead` (line 214–220): cache `state.firstSnapshot` on the
  first call within the transaction and reuse it.

**But the dispatcher at `internal/server/dispatch.go:295-300` re-invokes
`SnapshotFor` for every statement, bypassing whatever the MVCC layer
intends to do** — the `state.firstSnapshot` cache is consulted but the
caller already lost the ability to short-circuit at the call site.
The effect: even an RR transaction sees concurrently-committed writes
between two of its own SELECTs, breaking `eval-plan-qual`, `merge-match-recheck`,
and any spec whose expected output assumes RR semantics.

This is a dispatcher-side bug, not an MVCC-side one. The fix is small.

## Solution

### Carry the isolation level on `connTxState`

`internal/server/conn_tx.go` holds `connTxState.tx mvcc.Transaction` already.
Expose `tx.Isolation` (an existing field per `manager.go:48`) at the
dispatcher level so the per-statement loop can branch on it.

### Gate the per-statement refresh

In `internal/server/dispatch.go` around line 295–300, before calling
`s.cfg.TxnMgr.SnapshotFor(tx)`:

```go
// PG-parity: RC refreshes snapshot per statement; RR/SSI hold the
// BEGIN-time snapshot for the whole transaction.
if tx.Isolation == mvcc.ReadCommitted || ectx.Snap == nil {
    snap2, err := s.cfg.TxnMgr.SnapshotFor(tx)
    if err != nil {
        return s.writeQueryError(w, sqlstate.SystemError, err.Error())
    }
    ectx.Snap = snap2
}
// else: keep ectx.Snap as-is (BEGIN-time snapshot for RR/SSI).
```

Implicit-transaction path (auto-BEGIN-per-statement): unchanged. The
isolation level for an implicit txn is whatever the session default is
(default `ReadCommitted`), so the gate is a no-op there.

### `SET TRANSACTION ISOLATION LEVEL` after BEGIN

`SET TRANSACTION ISOLATION LEVEL` is only valid before the first query
of the transaction (PG raises `25001` if a snapshot was already taken).
The gate above only kicks in when `ectx.Snap != nil`, so the existing
M0096-0002 `SetIsolationLevel` semantics are preserved.

## Files touched

- `internal/server/dispatch.go` — single conditional around the
  `SnapshotFor` call site (~5 lines).
- `internal/server/conn_tx.go` — optional helper if `tx.Isolation` is
  not already reachable from the dispatcher hot path (likely a no-op
  since `tx` is already in scope).
- `internal/mvcc/manager.go` — no change. `SnapshotFor`'s cached-snapshot
  logic at lines 214–220 already does the right thing and is retained as
  the fallback for the first SELECT of an RR txn.

## Reference (upstream)

- `postgres/src/backend/utils/time/snapmgr.c` — `GetTransactionSnapshot`
  applies the per-isolation-level decision; `RegisterSnapshot` caches
  the RR snapshot at the first call after `BeginTransaction`.
- `postgres/src/backend/storage/lmgr/predicate.c` — SSI hooks on top
  of the same RR snapshot path. goopg does not yet implement SSI; the
  RR caching path is the correctness target.

## Verification

- New unit test in `internal/mvcc/manager_test.go` (or sibling): two
  goroutines, one runs `BEGIN ISOLATION LEVEL REPEATABLE READ` → SELECT,
  the other INSERT+COMMIT, first repeats SELECT and must see the same
  row count. RC variant must see the new row.
- `TestPort_IsolationEvalPlanQual` and `TestPort_IsolationMergeMatchRecheck`
  must advance past the snapshot-divergence step (verified by reading
  `Diff` output of `runIsoSpec`).
- `go test -race ./internal/server/... ./internal/mvcc/...` clean.

## Risks

- Stale `ectx.Snap` carried across an `END` + `BEGIN` boundary inside the
  same physical connection. Mitigated: `execCommit` / `execRollback` already
  resets `connTxState`; assert `ectx.Snap = nil` at end-of-transaction in
  the same path.
- Long-running RR transactions hold an old snapshot, blocking vacuum
  (`OldestXmin`). M0093 already tracks read-only RR snapshotXmin in
  `OldestXmin`; verify the cached `firstSnapshot.xmin` participates in
  the same accounting.
