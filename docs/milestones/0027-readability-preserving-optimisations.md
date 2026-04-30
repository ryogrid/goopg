# Milestone 0027 — Low-Risk Performance Optimisations (Readability-Preserving)

**Status:** planned
**Depends on:** Milestone 0026 (concurrent WAL append), Milestone 0025 (OLTP analysis — identified remaining overhead in the executor hot path).
**Drives:** Reduce per-tuple and per-operation CPU/memory overhead in the executor's scan, decode, and write paths without sacrificing code clarity.

## Context

The M0025/M0026 investigation resolved the two dominant bottlenecks
(lockScanMatch mutex and SeqScan for UPDATE). At ~1,500 TPS (simple
update, 4 clients) the profile is now CPU-bound rather than lock-
or I/O-bound. The remaining ~1.3 ms per UPDATE is split across:

- B-tree lookup (RangeScan)
- Heap tuple fetch + decode + visibility check
- New row construction (Row copy, multiple evalExpr calls)
- WAL record encoding (CRC computation per Append)

This milestone targets the **micro-optimisation layer**: reducing
memory allocations, eliminating redundant copies, and inlining hot
paths. Each change must preserve or improve readability.

## Candidate Optimisations

### 1. Pre-allocate `pending` slice in UPDATE/DELETE

`updateOp.Next()` builds a `pending` slice with `append`. For the
common case (single-row match), pre-allocating `pending: make(..., 0, 1)`
eliminates one grow+copy.

**Impact:** Tiny (< 1 %). **Readability:** Unchanged.

### 2. Avoid re-decoding tuple after lock release

`updateViaIndex` re-pins and re-decodes the heap tuple after a
foreign tuple lock is released. The re-decode is not always
necessary — if the tuple wasn't modified while we waited, the
original decode is still valid.

**Impact:** Small (~5 % for contended workloads). **Readability:** Minor.

### 3. Reuse `Row` buffers in scan loops

`scanMatching` allocates a new `Row` for every tuple via `DecodeRow`.
For the common case where only 1 tuple matches out of 300K scanned,
this is 300K short-lived allocations. Reusing a row buffer
(and calling `DecodeRowInto`) would reduce GC pressure.

**Impact:** Medium (~10–15 % for SeqScan paths). **Readability:** Moderate.

### 4. Inline `DecodeRow` for the hot path

`DecodeRow` iterates columns and converts `[]byte` → `Datum`. This
is called for every tuple in every scan. Providing a
`DecodeRowInto(dst Row, data []byte) error` variant that fills a
pre-allocated row slice avoids the allocation.

**Impact:** Medium (~10–15 % for SeqScan paths). **Readability:** Minor.

### 5. CRC-32 caching for WAL records

Each `encodeRecord` call computes `crc32.ChecksumIEEE(payload)`.
For small payloads (commit markers, heap delete records), the CRC
dominates the encoding time. Caching the CRC for the most recent
record (same payload → same CRC) would help when the same record
type is emitted frequently.

**Impact:** Small (~2–5 %). **Readability:** Minor addition.

### 6. Avoid `append` in `indexScanOp` row collection

`indexScanOp.Open()` builds `o.rows` via `append`. Pre-allocating
to capacity 1 (the common case for unique-index lookups) avoids
a grow+copy.

**Impact:** Tiny (< 1 %). **Readability:** Unchanged.

### 7. Reduce `Pool.Pin` hash-table lookups

`Pool.Pin` does a map lookup in `byTag`. For sequential page scans,
a simple "is the next block already pinned?" check could avoid the
lookup. However this increases code complexity significantly.

**Impact:** Medium (~10 % for scans). **Readability:** Significant.
**Deferred** to a follow-up milestone.

### 8. Use `sort.Search` in B-tree internal page traversal

`descendToLeaf` calls `findChildBlock` which does a linear scan of
internal page items. Since B-tree internal pages are sorted, a
binary search (`sort.Search`) would cut the scan from O(items) to
O(log items). For a fanout of ~200 items/page, this is ~7× fewer
comparisons per level.

**Impact:** Medium (~5–10 % for index lookups). **Readability:** Minor.

### 9. Eliminate `MarshalBinary` call in `PageAddHeapTuple`

`PageAddHeapTuple` calls `t.MarshalBinary()` to get the raw tuple
bytes, but `tryAppendToBlock` already called it when constructing
the tuple. Passing raw bytes instead of a `HeapTuple` would avoid
a second serialisation.

**Impact:** Small (~2 %). **Readability:** Moderate (API change).

## Prioritisation for v0

| # | Optimisation | Est. Impact | Effort | Risk | Priority |
|---|-------------|-------------|--------|------|----------|
| 3 | `DecodeRowInto` — reuse row buffer | 10–15 % | Low | Low | P1 |
| 6 | Pre-allocate pending/rows slices | < 1 % | Trivial | None | P1 |
| 8 | `sort.Search` in B-tree descent | 5–10 % | Low | Low | P1 |
| 5 | CRC-32 caching for WAL | 2–5 % | Low | Low | P2 |
| 2 | Avoid re-decode after lock release | 5 % | Low | Low | P2 |
| 9 | Eliminate redundant MarshalBinary | 2 % | Medium | Medium | P3 |
| 7 | Pool.Pin hash-skipping | 10 % | High | High | P4 |

## Out of Scope

- Full query plan caching (planner optimisation).
- Buffer pool prefetching strategy changes.
- Go GC tuning (`GOGC`, `memoryLimit`).
- Index type or storage engine changes.
- Parallel query execution.
- JIT compilation or assembly optimisation.

## Definition of Done

1. Each candidate optimisation is evaluated and either implemented
   or explicitly deferred with rationale.
2. At least three of the P1 items are implemented.
3. `go test ./...` remains green.
4. `benchstat` or equivalent shows measurable improvement
   (≥ 5 %) for the simple-update workload at scale=3, 4 clients.
5. A brief report is added to `analysis/oltp-performance/`
   documenting which optimisations were applied and their impact.

## Reference

- Hot-path trace from `go tool pprof` during pgbench simple-update
  workload (scale=3, 4 clients).
- `internal/executor/operators_storage.go` — scanMatching, updateOp/deleteOp
- `internal/executor/operators_index.go` — indexScanOp
- `internal/storage/heap.go` — PageAddHeapTuple
- `internal/wal/format.go` — encodeRecord
- `internal/access/btree/btree.go` — descendToLeaf
