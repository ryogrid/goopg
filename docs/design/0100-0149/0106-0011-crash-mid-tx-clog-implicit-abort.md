# 0106-0011 — Crash-Mid-Transaction Catalog Heap Filter (Implicit Abort)

**Status:** accepted
**Milestone:** M0106-0011 (operational relcache/catcache maintenance)
**Date:** 2026-05-20
**Predecessor:** [0106-0011-rollback-catalog-rows-clog-filter.md](0106-0011-rollback-catalog-rows-clog-filter.md)

## Problem

The previous loop ([0106-0011-rollback-catalog-rows-clog-filter](0106-0011-rollback-catalog-rows-clog-filter.md))
closed the explicit-ROLLBACK case by filtering pg_class / pg_attribute rows
whose `xmin` is `TxnStatusAborted` in the local clog. The implicit-abort
sibling test, `TestCrashMidTransactionTableNotVisibleAfterRestart`, still
failed with two distinct symptoms:

1. WAL replay aborted with
   `wal: replay record 40 lsn[…]: wal: btree-newroot trailing bytes (68 remaining)`
   before `loadUserTablesFromHeap` ever ran.
2. After the replay fault was fixed, the crashed-in-progress `crash_ghost`
   table reappeared in the live catalog on restart.

## Root causes

### 1. WAL classifier byte-collision misroute

`wal.classifyXLogRecord` returns `(RmgrXLog, xlogCheckpointShutdown, 0)`
for any 88-byte payload — the size of the PG-canonical `CheckPoint` struct
(M0102-0007). When the redo-LSN's low byte in a CheckPoint payload happens
to equal a known goopg native record kind (here `RecordKindBtreeNewRoot = 24
= 0x18`), `ApplyRecord` saw `r.Payload[0] == 0x18`, decided it was a native
record, and dispatched into the BtreeNewRoot decoder. The decoder read the
first 20 bytes successfully (count=0) and surfaced the remaining 68 bytes
as "trailing bytes".

### 2. Implicit-abort path was missing

`loadUserTablesFromHeap`'s filter (post-loop 30) only excluded rows whose
`xmin` was explicitly `TxnStatusAborted`. A transaction that wrote pg_class
rows but crashed before any `XactCommit`/`XactAbort` marker reached disk
leaves no clog entry for its xid — `clog.GetStatus` returns
`TxnStatusUnknown`. The loop-30 design doc assumed an "existing
implicit-abort path stamps such xids as Aborted before
`loadUserTablesFromHeap` runs" but no such path existed.

A secondary subtlety: the catalog snapshot saves `NextXID` only on clean
shutdown, so on crash recovery `txnMgr.NextXID()` can be lower than the
maximum xid actually present in the heap. The implicit-abort sweep must
therefore size its upper bound from the on-disk heap, not from `txnMgr`.

## Fix

### Part 1 — `internal/wal/recovery.go` (`ApplyRecord`)

When `r.XLog` is set, prefer the structured-PG dispatch path whenever the
header advertises a non-default classification. Goopg-native records always
classify with `RmgrXLog` + `xlogInfoDefault` (`0xF0`); any other Rmgr or
Info value means the classifier produced a structured PG-canonical
classification (Checkpoint, canonical envelope, etc.) and the byte at
`payload[0]` is part of that structured payload — not a record-kind tag.

```go
if r.XLog.Header.Rmid != RmgrXLog || r.XLog.Header.Info != xlogInfoDefault {
    return replayDecodedXLogRecord(mgr, r)
}
```

### Part 2 — `internal/mvcc/clog.go` (`MarkUnknownAsAborted`)

New method that walks `[1, highXID)` and stamps any `TxnStatusUnknown`
slot as `TxnStatusAborted`, growing `c.data` to cover the bound. Mirrors
PostgreSQL's "any non-Committed CLOG slot is treated as not committed"
semantics for crash recovery and integrates with the existing PG SLRU
mirror via `mirrorToSLRULocked`.

### Part 3 — `internal/initdb/open.go` (`Open`)

Between the legacy clog-empty upgrade path and `loadUserTablesFromHeap`,
size the implicit-abort sweep correctly:

1. `highestCatalogXID(mgr, cat)` scans `pg_class.MainFork` and
   `pg_attribute.MainFork` for the maximum `xmin`/`xmax` seen on the
   restored heap pages. Returns 0 when the heap is absent (fresh initdb).
2. If the observed max xid is >= `txnMgr.NextXID()`, advance `NextXID`
   so the sweep covers it.
3. Call `clog.MarkUnknownAsAborted(txnMgr.NextXID())`.

The sweep only runs on the non-empty-clog branch (the empty-clog branch is
the legacy upgrade path that marks everything Committed and does not need
the implicit-abort treatment).

## Why filter-only changes are not enough

A purely filter-side change ("treat Unknown as Aborted in the visibility
test") would still leave the bare clog inconsistent for any other code
path that consults `clog.GetStatus(xid)` for the same crashed xid — for
instance, the MVCC visibility check used by the runtime, or any future
recovery-correctness check. Stamping the slot at Open time makes the clog
the single source of truth and avoids divergent semantics across call
sites.

## Basebackup considerations

The sweep marks every Unknown slot in `[1, NextXID)` as Aborted. For a
local cluster (`Init` + `Open`), the only Unknown slots are crashed
in-progress xids — exactly what we want filtered.

For a future basebackup-attached cluster, upstream xids that pre-date the
attach are not present in our local clog and would be incorrectly marked
Aborted by the sweep. The new `CLog.MarkUnknownAsAborted` doc-comment
documents the requirement: basebackup attach must call
`InitializeAsCommitted(upstream_nextXid)` before this point so the
upstream range is already Committed and the sweep skips those slots.

The current goopg standby code path (M0094 still in progress) does not
yet exercise this case, so the sweep does not regress any existing test
matrix; the dependency is documented for the basebackup work to honour
when it lands.

## Tests

- `internal/wal/xlog_replay_test.go::TestApplyRecordPrefersDecodedXLogForStructuredInfo`
  — pins the byte-collision misroute fix using an 88-byte payload whose
  first byte equals `RecordKindBtreeNewRoot`. Without the fix, ApplyRecord
  routes into `replayBtreeNewRoot` and fails with "trailing bytes (68
  remaining)".
- `internal/mvcc/clog_test.go::TestCLogMarkUnknownAsAborted` — pins the
  sweep semantics: pre-existing Committed/Aborted untouched, Unknown
  stamped Aborted, grown slots default-then-stamped, persistence across
  `OpenCLog` re-open.
- `internal/mvcc/clog_test.go::TestCLogMarkUnknownAsAbortedZeroBound`
  — no-op bound is a no-op (no file rewrites).
- `internal/initdb/clog_crash_test.go::TestCrashMidTransactionTableNotVisibleAfterRestart`
  — end-to-end recovery test: BEGIN; CREATE TABLE; hard-close; reopen;
  table absent. Was the surfacing failure (FAIL on master, PASS now).
- `internal/initdb/clog_crash_test.go::TestCommittedTableSurvivesCrashRestart`
  — guards against the sweep being too aggressive (auto-commit CREATE
  TABLE stays visible). Was PASS, still PASS.
- `internal/initdb/transactional_ddl_test.go::TestRollbackedTableNotVisibleAfterRestart`
  — guards the explicit-rollback path (loop 30's fix). Was PASS, still
  PASS.

## Verification

```
$ go test -count=1 -run TestCrashMidTransactionTableNotVisibleAfterRestart \
    ./internal/initdb/            # PASS (was FAIL on master)
$ go test -count=1 ./internal/initdb/   # 15 pre-existing failures unchanged
                                         # (TestCrashMid… flipped FAIL→PASS)
$ go test -count=1 ./internal/mvcc/     # all PASS (incl. new sweep tests)
$ go test -count=1 ./internal/wal/      # 2 pre-existing failures unchanged
                                         # (TestApplyRecordPrefersDecodedXLogForStructuredInfo PASS)
$ go test -count=1 ./internal/executor/ # all PASS
$ go test -count=1 ./internal/catalog/  # all PASS
$ go test -count=1 ./internal/server/   # all PASS
```
