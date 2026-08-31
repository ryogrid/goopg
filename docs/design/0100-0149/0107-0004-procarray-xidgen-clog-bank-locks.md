# 0107-0004 — Phase D1: ProcArray + Atomic XidGen + CLOG Bank Locks

**Status:** accepted  
**Milestone:** M0107-0004  
**Date:** 2026-05-21

## Summary

Replaces `mvcc.Manager`'s single `sync.Mutex` hot path (Begin / SnapshotFor / Commit) with three independent lock-free primitives, eliminating the dominant MVCC bottleneck at high concurrency.

Evidence from `analysis/perf-optimize/04-contention.md`:
- c=50 SU block time: `SnapshotFor` 62.73 %, `Begin` 6.97 %, `Commit` 6.23 %
- c=50 SU mutex-unlock time: `Commit/finish` 92.48 %

## Changes

### 1. `internal/mvcc/procarray.go` (new)

64-byte cache-line–aligned `procSlot` per active backend:

```
offset  0: xid        atomic.Uint64  — write-XID; 0 = no active write-xact
offset  8: xmin       atomic.Uint64  — lowest Xmin seen; ^uint64(0) = none
offset 16: firstSnap  *Snapshot      — RR/SSI pinned snapshot
offset 24: snapshotXmin uint32
offset 28: isolation  int32
offset 32: inTxn      atomic.Uint32  — 1 = active txn; 0 = idle
offset 36: _pad       [28]byte
```

`ProcArray` holds a `[]procSlot` of `DefaultProcArraySize = 1024`.

### 2. `internal/mvcc/xidgen.go` (new)

`XidGen` wraps `atomic.Uint64` with pre-increment semantics:
- `next` stores the **next XID to assign** (initial value = `FirstNormalTransactionID = 3`)
- `Allocate()` = `next.Add(1) - 1` → returns current-then-advances
- `Peek()` = `next.Load()` → "what Allocate would return next"
- `SetNext(x)` = monotonic CAS advance

### 3. `internal/mvcc/clog.go` (rewritten)

Single `sync.RWMutex` replaced by per-bank locking:
```
const xidsPerBank = 128 * 1024
type clogBank struct { mu sync.RWMutex; data []byte }
type CLog struct { path string; banks []*clogBank; banksMu sync.RWMutex; slruDir string }
```
`SetCommitted`/`SetAborted` only contend on `banks[xid/xidsPerBank].mu`. `GetStatus` takes only the bank's `RLock`. The PG SLRU mirror and flat-file persistence are unchanged.

### 4. `internal/mvcc/manager.go` (major refactor)

Removed: `mu sync.Mutex`, `active map[TxnHandle]*txState`, `nextHandle TxnHandle`, `nextXID TransactionID`.

Added:
- `procArray ProcArray` — per-backend slot array
- `xidgen XidGen` — atomic XID allocator
- `abortedMu sync.RWMutex` — protects `abortedXIDs` only
- `xactMarkerMu sync.RWMutex` — protects xactMarker callback
- `waitMu sync.Mutex` + `commitCond` — WaitForXID only
- `ssiMu sync.Mutex` — SSI/predlock cold path
- `autoProcNum atomic.Int32` — auto-assignment for callers without explicit procNum

Key behavioral changes:
- `Begin(iso, procNums ...int32)` — variadic; production callers supply explicit procNum, tests/background workers get auto-assigned
- `Handle = TxnHandle(procNum+1)` — ensures Handle ≥ 1 (0 remains "invalid")
- `SnapshotFor` — lock-free ProcArray walk; no mutex
- `Commit/Rollback` — slot clear (atomic) + conditional abortedMu + ssiMu + waitMu broadcast
- `OldestXmin` — lock-free walk, skips slots with `inTxn=0` (idle slots have xmin=0 and would falsely pin vacuum at 0)
- `IsXIDActive` — lock-free slot walk
- `WaitForXID` — uses dedicated `waitMu`/`commitCond`

### 5. Server threading

- `connTxState.ProcNum int32` — computed at connect as `(pid-1) % DefaultProcArraySize`
- `executor.Context.ProcNum int32` — wired by `dispatchSimpleQueryViaExecutor`
- `dispatch.go`, `operators_tx.go` — pass `ProcNum` to `TxnMgr.Begin`

## Critical bug fix during implementation

`OldestXmin()` must skip idle slots (`inTxn == 0`). Idle slots have `xmin = 0` (Go zero value for `atomic.Uint64`), which is not `^uint64(0)` (the sentinel for "no snapshot taken"), so the original walk would set `OldestXmin = 0` on a freshly-initialized Manager, causing VACUUM to prune all heap tuples as if they were universally dead.

## Correctness invariants

- `Begin` sets `s.xmin.Store(^uint64(0))` (not 0) so SnapshotFor's CAS-update path works
- `finish` restores `s.xmin.Store(^uint64(0))` on Commit/Rollback
- `captureSnapshot` only reads `s.xid` (not xmin); idle slots with `xid=0` are correctly skipped
- `WaitForXID` + `finish` maintain the standard condition-variable no-lost-wakeup invariant via `waitMu` ordering

## Verification

`go test -race -count=1 ./internal/mvcc/ ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/wal/ ./internal/vacuum/ ./internal/planner/ ./internal/parser/ ./internal/analyzer/ ./internal/mctx/ ./internal/access/btree/` — all PASS.

Pre-existing failures in `internal/initdb/` (heap-tuple decode issue, unrelated to this change) are unchanged.
