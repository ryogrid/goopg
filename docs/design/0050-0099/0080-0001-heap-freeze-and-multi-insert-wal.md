# 0080-0001 — Heap FREEZE + remaining heap WAL parity

| field | value |
| --- | --- |
| status | draft |
| date | 2026-05-11 |
| scope | wal, storage, vacuum |
| related | 0079-0001 (catalog DDL recovery), 0079-0002 (btree
vacuum WAL), 0079-0003 (btree page-deletion WAL), 0046-0005
(VACUUM FREEZE) |

## 1. Problem statement

After M0079-0001 / 0002 / 0003 closed the btree-side WAL parity
gaps, an audit of the heap-side surface against PostgreSQL's
`heapam_xlog.h` produced this status:

| PostgreSQL XLOG_HEAP / XLOG_HEAP2 | goopg counterpart | Status |
| --------------------------------- | ----------------- | ------ |
| INSERT | `RecordKindHeapInsert` (4) | ✅ |
| DELETE (xmax stamp) | `RecordKindHeapDelete` (6) | ✅ |
| HOT_UPDATE | `RecordKindHeapHotUpdate` (13) | ✅ atomic |
| UPDATE (non-HOT atomic) | HeapDelete + HeapInsert pair | 🟡 correct, not atomic — **WAL volume penalty** |
| LOCK | `RecordKindHeapLock` (10) | ✅ |
| TRUNCATE | `RecordKindSmgrTruncate` (12) | ✅ functionally equivalent |
| INPLACE | not present | 🟡 N/A (goopg has no system-catalog inplace path) |
| CONFIRM | not present | 🟡 N/A (goopg's INSERT ... ON CONFLICT path uses regular HeapInsert + delete) |
| HEAP2 PRUNE_VACUUM_SCAN (page prune) | `RecordKindHeapVacuum` (7) | ✅ |
| HEAP2 PRUNE_ON_ACCESS (opportunistic) | `RecordKindHeapPruneOpt` (14) | ✅ |
| **HEAP2 FREEZE_PAGE** | FPI fallback via `MarkDirty` | ❌ **gap** |
| HEAP2 VISIBLE | not WAL-logged; in-memory `VisibilityMap` only | 🟡 acceptable — VM is recomputed by next VACUUM after crash |
| HEAP2 MULTI_INSERT | per-tuple HeapInsert pairs | 🟡 optimization, not bug |
| HEAP2 LOCK_UPDATED | not present | 🟡 N/A (goopg has no multi-row update locking yet) |
| HEAP2 NEW_CID | not present | 🟡 N/A (goopg's logical decoding doesn't track CID) |
| HEAP2 REWRITE | not present | 🟡 N/A (goopg's CLUSTER / VACUUM FULL not implemented) |

The single ❌ is **HEAP FREEZE**. `PageFreezeOldTuples`
(`internal/storage/freeze.go`) rewrites tuple xmins to
`FrozenTransactionID`, but the call site in
`internal/vacuum/vacuum.go:157` emits only a `pool.MarkDirty(slot)`
with the comment "conservative FPI for freeze". This relies on
the per-checkpoint-epoch FPI to capture the page's post-freeze
content, costing 8 KiB per frozen page vs. PostgreSQL's
`xl_heap_freeze_page` (~16-200 bytes per page).

## 2. Scope of M0080-0001

This slice converts the FREEZE-time page mutation to a logical
WAL record. The remaining 🟡 entries in the audit are either
N/A (no producer site exists yet) or optimization-only (no
correctness benefit at this time):

- **Atomic non-HOT UPDATE**: goopg's delete+insert pair is
  semantically correct. PostgreSQL's atomic `XLOG_HEAP_UPDATE`
  saves WAL volume by avoiding the duplicate header. M0080-0002
  candidate.
- **MULTI_INSERT**: pgbench / TPC-H workloads don't COPY-load
  in goopg's current code path, so the per-tuple HeapInsert
  pattern doesn't measurably affect benchmark numbers.
  M0080-0003 candidate.
- **VISIBLE / NEW_CID / REWRITE / LOCK_UPDATED / INPLACE /
  CONFIRM**: producer paths absent or out of scope.

## 3. PostgreSQL reference (heapam_xlog.h)

PostgreSQL 17+ folded `XLOG_HEAP2_FREEZE_PAGE` into
`XLOG_HEAP2_PRUNE_VACUUM_SCAN`. Earlier versions (and the
documentation) have:

```c
typedef struct xl_heap_freeze_plan {
    TransactionId xmax;
    uint16        t_infomask2;
    uint16        t_infomask;
    uint8         frzflags;
    uint16        ntuples;
    /* OffsetNumber tuple offsets follow */
} xl_heap_freeze_plan;
```

The PostgreSQL record is more general — it carries plan groups
where each group describes a transformation (e.g., "set xmin =
FrozenXID, clear infomask bits 0xX, set 0xY") and the offsets
that the transformation applies to. goopg's freeze is currently
narrower: only xmin → FrozenTransactionID. So the goopg record
can be simpler.

## 4. goopg `RecordKindHeapFreeze` (26)

### Format

```
kind(1) | DBOid(4) | RelOid(4) | Fork(1) | Block(4) |
count(2) | slots[count](2 each)
```

Each slot number is 1-based and refers to a `LP_NORMAL` line
pointer whose tuple's xmin was rewritten to
`FrozenTransactionID`.

Total size: 16 + 2 × count bytes (mirrors `RecordKindHeapVacuum`'s
header shape).

### Producer

`PageFreezeOldTuples` is extended to return the list of frozen
slot numbers via `PageFreezeStats.FrozenSlots []uint16`. The
producer is `internal/vacuum/vacuum.go::VacuumWithOptions`
line 155-160; when `LogHeapFreeze` is wired, it emits one
record per page with `Frozen > 0`. Falls back to
`pool.MarkDirty(slot)` (FPI fallback) when the hook is unset.

### Replay

```go
func replayHeapFreeze(mgr *storage.Manager, r Record) error {
    rel, blk, slots, err := DecodeHeapFreeze(r.Payload)
    page := readPage(rel, blk)
    if page.LSN >= r.EndLSN { return nil } // pd_lsn idempotency
    storage.PageFreezeBySlots(page, slots)  // new helper
    page.SetLSN(r.EndLSN)
    writePage(rel, blk, page)
}
```

`PageFreezeBySlots` is the deterministic replay kernel — same
relationship to `PageFreezeOldTuples` as
`VacuumHeapPageBySlots` has to `VacuumHeapPage`.

## 5. Hook wiring

```go
// storage/bufpool.go
type LogHeapFreezeFunc func(rel RelFileNode, blk BlockNumber, frozenSlots []uint16) (LSN, error)
func (p *Pool) LogHeapFreeze() LogHeapFreezeFunc

// initdb/open.go
logHeapFreeze := func(rel storage.RelFileNode, blk storage.BlockNumber, slots []uint16) (storage.LSN, error) {
    payload := wal.EncodeHeapFreeze(rel, blk, slots)
    _, end, err := walWriter.Append(payload)
    return storage.LSN(end), err
}
```

## 6. Tests

- `internal/wal/heap_freeze_test.go`: encode/decode round-trip,
  truncated-payload + wrong-kind guards.
- `internal/storage/freeze_test.go`: extend the existing tests
  to assert `FrozenSlots` accuracy.
- `internal/vacuum/vacuum_test.go`: add a capture-hook test that
  asserts `LogHeapFreeze` fires with the expected slot list when
  `FreezeBelow` is set.

## 7. Acceptance

- All tests in §6 green.
- A synthetic VACUUM FREEZE workload (10 pages, 100 tuples each,
  half eligible for freezing) emits 10 logical records totalling
  ~1.6 KiB instead of 80 KiB of FPIs.
- Existing freeze-related tests
  (`TestPageFreezeOldTuples*`, vacuum integration) continue to
  pass without modification — the producer-side fallback ensures
  semantic invariance when the hook is unwired.

## 8. Out of scope

- Atomic non-HOT UPDATE (`XLOG_HEAP_UPDATE`).
- MULTI_INSERT (`XLOG_HEAP2_MULTI_INSERT`).
- VM-visible WAL (`XLOG_HEAP2_VISIBLE`) — would require persistent
  VM. Currently VM is reconstructed by VACUUM after a crash.
- Multi-tuple lock state (`XLOG_HEAP2_LOCK_UPDATED`) — depends on
  future multi-row locking work.
