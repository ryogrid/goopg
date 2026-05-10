# 0080-0002 — Remaining PG-parity WAL records (atomic UPDATE / MULTI_INSERT / VISIBLE / REUSE_PAGE / META_CLEANUP)

| field | value |
| --- | --- |
| status | in progress (record infrastructure landed; producer wiring + VM/FSM persistence in progress) |
| date | 2026-05-11 |
| scope | wal, storage, vacuum |
| related | 0079-0001..0004 (catalog + btree page-deletion WAL),
0080-0001 (heap freeze) |

## 1. Problem statement

After M0079 + M0080-0001, six PostgreSQL WAL record kinds remain
unimplemented in goopg. The user explicitly asked for parity on all
of them plus an audit of "PostgreSQL で永続性が保証されているにもかかわらず、
goopg で保証されていないもの":

| PG record | Purpose | goopg status |
| --------- | ------- | ------------ |
| `XLOG_HEAP_UPDATE` | atomic non-HOT UPDATE | infra ✅ (`RecordKindHeapUpdate` 27) — producer wiring deferred |
| `XLOG_HEAP2_MULTI_INSERT` | bulk INSERT | infra ✅ (`RecordKindHeapMultiInsert` 28) — producer wiring deferred |
| `XLOG_HEAP2_VISIBLE` | VM bit set/clear | infra ✅ (`RecordKindHeapVisible` 29) — producer wiring + persistent VM deferred |
| `XLOG_BTREE_REUSE_PAGE` | page recycle notification | infra ✅ (`RecordKindBtreeReusePage` 30) — producer wiring deferred |
| `XLOG_BTREE_META_CLEANUP` | metapage cleanup-XID update | infra ✅ (`RecordKindBtreeMetaCleanup` 31) — producer wiring deferred |
| `XLOG_HEAP_INPLACE` | system-catalog inplace update | N/A (goopg has no inplace path) |

## 2. Persistence audit

PostgreSQL persists the following metadata across crashes; the
goopg equivalents are:

| PG | goopg | Status |
| -- | ----- | ------ |
| `pg_xact` (clog) | `internal/mvcc/clog.go` → `<DataDir>/global/pg_xact` | ✅ persistent (full file rewrite per status change — inefficient but correct) |
| `pg_catalog` heaps + system catalog state | `<DataDir>/global/pg_catalog.json` + heap relfiles + WAL records (M0079-0001) | ✅ persistent |
| `pg_wal/...` | `<DataDir>/pg_wal/...` | ✅ persistent |
| `pg_replslot/...` | `internal/wal/slots.go` (atomic temp+rename) | ✅ persistent |
| heap / index relfiles | `<DataDir>/base/<DBOid>/<RelOid>` | ✅ persistent |
| `pg_visibility` (VM fork) | `internal/storage/vm.go` (in-memory `VisibilityMap`) | ❌ **gap — VM lost across restart** |
| `pg_freespace` (FSM fork) | `internal/storage/fsm.go` (in-memory `FSM`) | ❌ **gap — FSM lost across restart** |
| `pg_subtrans` | rebuilt from `RecordKindXactAssignment` records | ✅ via WAL replay |
| `pg_multixact` | not implemented | 🟡 N/A (no multi-row locking yet) |
| `pg_twophase` | not implemented | 🟡 N/A (no PREPARE TRANSACTION) |
| `pg_commit_ts` | not implemented | 🟡 N/A (optional in PG) |

The two ❌ gaps are VM and FSM. Both are recovered functionally
(VACUUM rebuilds VM bits via `PageAllVisible`; FSM is rebuilt
opportunistically on first INSERT failure → page extension), so a
crash today doesn't lose CORRECTNESS — only the optimisation
metadata. PostgreSQL persists them in fork files for performance.

## 3. M0080-0002 scope (record infrastructure — landed)

Five new WAL record kinds with full encode/decode + replay
infrastructure + ApplyRecord routing landed in this slice:

- `RecordKindHeapUpdate` (27): combined old-tuple xmax stamp +
  new-tuple insert in one atomic record. Replay applies each half
  under independent pd_lsn idempotency. Format:
  `kind|rel|oldBlk|oldSlot|xmax|newBlk|newSlot|tupleLen|tupleBytes`.
- `RecordKindHeapMultiInsert` (28): N tuples to the same heap
  page in one record. Format: `kind|rel|blk|count|[(slot,len,bytes)*]`.
- `RecordKindHeapVisible` (29): VM bit set/clear with cutoff
  XID. Format: `kind|rel|heapBlk|flags|cutoffXid`. Flags: `0x01
  setAllVisible`, `0x02 setAllFrozen`.
- `RecordKindBtreeReusePage` (30): page-recycle notification
  for hot-standby readers (when goopg gains them). Format:
  `kind|rel|blk|recycledFromXid`.
- `RecordKindBtreeMetaCleanup` (31): metapage cleanup-XID +
  tuple counters. Format: `kind|rel|numHeapTuples|lastCleanupNumDeletedTuples`.

Replay for HeapUpdate + HeapMultiInsert is full producer-driven
(reads page, applies mutation, sets pd_lsn, writes back). Replay
for HeapVisible / BtreeReusePage / BtreeMetaCleanup is no-op
(catalog/metadata-only — see ApplyRecord case in
`internal/wal/recovery.go`). When goopg gains:

- a persistent VM, the HeapVisible replay flips the VM-fork bit;
- hot-standby reads, BtreeReusePage replay locks out concurrent
  readers on the recycled page;
- a persistent FSM that tracks cleanup-XIDs, BtreeMetaCleanup
  replay updates the metapage's cleanup-XID slot.

## 4. Deferred work (M0080-0003+)

### 4.1 Atomic non-HOT UPDATE producer (M0080-0003a)

Refactoring `updateOp.Next()` (currently emits HeapDelete +
HeapInsert pair) to use the new `RecordKindHeapUpdate`. Requires:

- Pin both old and new pages under simultaneous content latches.
  Lock-ordering hazard: when old.Block != new.Block, the locking
  order must be deterministic (lowest block first) to avoid
  deadlocks vs concurrent UPDATEs in the opposite direction.
- Emit ONE WAL record covering both halves.
- Apply each mutation with `MarkDirtyWithLSNLocked`.

### 4.2 MULTI_INSERT producer (M0080-0003b)

Refactor COPY / bulk INSERT (`internal/executor/operators_copy.go`)
to batch tuples per page and emit `RecordKindHeapMultiInsert`.
Acceptance: a 1000-row COPY emits 10-20 MultiInsert records (one
per filled page) instead of 1000 HeapInsert records.

### 4.3 Persistent VM (M0080-0003c)

VM fork pages on disk (`<DataDir>/base/<DBOid>/<RelOid>_vm`).
Format options:

- **Simple**: 1 byte per heap page (true/false ALL_VISIBLE). Easy
  to implement; matches goopg's current in-memory layout.
- **PG-aligned**: 2 bits per heap page (ALL_VISIBLE + ALL_FROZEN),
  packed 4 pages per byte. 8x compression vs simple but adds bit
  manipulation.

The MVP is the simple format. Producer changes:

- VACUUM emits `RecordKindHeapVisible(setAllVisible, cutoffXid)`
  before flipping the bit.
- Heap INSERT/DELETE/UPDATE paths emit
  `RecordKindHeapVisible(flags=0)` to clear the bit.

Replay: maintain bit on disk; readers consult on-disk state.

### 4.4 Persistent FSM (M0080-0004a)

FSM fork pages on disk
(`<DataDir>/base/<DBOid>/<RelOid>_fsm`). Format: 2 bytes per heap
page (free-space approximation). Producer changes:

- VACUUM updates the FSM bit after page prune; emits a WAL record
  if accuracy across crashes matters (vs lazy-rebuild on first
  insert miss).
- Heap INSERT decrements free-space estimate; no WAL needed (FSM
  is conservative — can be stale).

### 4.5 BTREE_REUSE_PAGE producer (M0080-0004b)

When goopg's btree page recycle (`recycleBlock`) reuses a page,
emit `RecordKindBtreeReusePage`. Currently goopg's recycle is
purely in-memory; no producer site exists. The infrastructure
records wait for hot-standby read paths.

### 4.6 BTREE_META_CLEANUP producer (M0080-0004c)

PG uses metapage cleanup-XID for index-vacuum sequencing
(`vacuumlazy.c::lazy_cleanup_one_index`). goopg currently has no
cleanup-XID concept; this is M0080+ depending on whether index
auto-vacuum scheduling needs it.

## 5. Acceptance for the M0080-0002 record-infrastructure slice

- ✅ All 5 record kinds compile + encode/decode round-trip.
- ✅ ApplyRecord routes each kind to the correct handler (or
  no-op) without regressing other record types.
- ✅ Existing tests in `internal/wal`, `internal/storage`,
  `internal/access/btree`, `internal/initdb`, `internal/vacuum`
  continue to pass.
- 🟡 Producer wiring + persistent VM/FSM = M0080-0003/0004
  follow-up slices.

## 6. Risk register

| # | Risk | Mitigation |
| - | ---- | ---------- |
| R1 | Atomic UPDATE producer hits lock-ordering hazard between concurrent UPDATEs to swapped page pairs | Block-number ordering rule (lowest first); existing concurrent-UPDATE tests cover this |
| R2 | MULTI_INSERT batch overflows page mid-record | Producer must close the batch before extending; per-record emission rather than transactional batching |
| R3 | Persistent VM fork file corruption (e.g., torn writes) | Use the same atomic temp+rename pattern as `internal/wal/slots.go` |
| R4 | Persistent FSM accuracy after crash drifts from heap reality | Lazy-rebuild on first insert miss is sufficient; full crash-safe FSM is M0080+ |
| R5 | M0080-0002 records not consumed yet | Records are catalog/metadata-only or producer-deferred; landing the infrastructure first lets future producer slices be drop-in additions |
