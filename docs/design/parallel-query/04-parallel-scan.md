# 04 — Parallel Sequential Scan

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-21 |
| depends on | [03](03-concurrency-substrate.md) |

`Parallel Seq Scan` is the foundation: it is the only node in the TPC-H
reference set that actually *divides* work (26 occurrences), and every other
parallel node in this bundle is downstream of it. It is also the smallest
change in the bundle, because `seqScanOp`'s iteration state is already the
right shape.

## 1. Why the existing scan is nearly ready

`seqScanOp.Next` (`internal/executor/operators_storage.go:1389`) drives a
strictly forward, never-backtracking block cursor:

```
if o.pinned == nil && o.activePage == nil {
    if o.curBlock >= o.nBlocks { return nil, EOF }
    … cancellation poll …
    … acquire page (ring or Pool.Pin) …
    … read line-pointer count under a brief RLock into o.slotMax …
}
… per-tuple loop over o.curSlot …
```

with `o.curBlock++` at `:1434` (empty/new page) and `:1709` (page exhausted).

Three properties make this directly parallelisable:

1. **`nBlocks` is captured once at Open**, so the scan boundary is stable and
   all workers agree on it.
2. **The cursor never goes backwards**, so "hand out the next block" is a
   complete description of the work split.
3. **Rows already leave the operator defensively copied.** The decode buffer
   `scanRow` is documented (`:781-789`) as decoded in place with a `cloneRow`
   returned so retaining callers keep their own copy.

Property 3 is *helpful but not sufficient* — `cloneRow` is shallow and
preserves `ArenaID`, so the [03](03-concurrency-substrate.md) §3 materialisation
requirement still applies at the worker's output boundary. It does mean the
scan itself needs no change on that axis.

## 2. The shared block allocator

Replace the per-operator `curBlock` cursor with a shared allocator when the
scan is parallel:

```go
// One instance per parallel scan node, created by the leader,
// referenced by every worker's seqScanOp.
type parallelScanState struct {
    next    atomic.Uint64      // next unallocated block
    nBlocks storage.BlockNumber // captured once, immutable
}

// Returns the next block to scan, or false when the relation is exhausted.
func (s *parallelScanState) nextBlock() (storage.BlockNumber, bool) {
    b := s.next.Add(1) - 1
    if b >= uint64(s.nBlocks) {
        return 0, false
    }
    return storage.BlockNumber(b), true
}
```

`seqScanOp` gains one field (`pscan *parallelScanState`, nil for a serial
scan). The two `o.curBlock++` sites and the `o.curBlock >= o.nBlocks`
termination check consult it when non-nil; everything else is untouched.

This is PG's `ParallelBlockTableScanDesc` model with the synchronisation
reduced to a single atomic increment — PG needs a spinlock because the state
also carries the sync-scan start position and a phase counter for the wraparound
sequential-scan optimisation, neither of which goopg has.

### 2.1 Chunked allocation

Handing out one block at a time costs one atomic per 8 KiB of data, which is
negligible against the page read, so **v1 allocates one block at a time**.

PG allocates in chunks that shrink as the scan nears the end
(`table_block_parallelscan_nextpage` with `PARALLEL_SEQSCAN_RAMPDOWN_CHUNKS`),
which exists to amortise the spinlock and to avoid a straggler holding a large
chunk at the end. goopg's per-block atomic makes the first motivation moot, and
single-block allocation makes the second impossible by construction. If
profiling later shows the atomic mattering, chunking is a local change behind
the same interface — recorded in [10](10-roadmap.md) rather than pre-optimised.

## 3. What stays per-worker

Everything else in the scan's state, and this is the part that needs care
rather than the allocator. Per [01](01-current-state-and-gap-analysis.md) B1
and B6:

| Field | Why per-worker |
| --- | --- |
| `pinned`, `activePage` | Each worker pins its own page; shared pins would break the balance discipline ([03](03-concurrency-substrate.md) §6.3), where `Unpin` underflow **panics** (`bufpool.go:1918-1930`) |
| `curSlot`, `slotMax` | Position within the worker's current page |
| `scanRow` | Decode buffer, reused per `Next()` |
| `slot` (embedded `MaterializedSlot`) | Returned slot, aliases `scanRow` |
| `sctx` (`*mctx.Context`) | Per-page arena. `Alloc` is an unsynchronised bump pointer (`mctx.go:254`); sharing one across workers is a hard data race |
| `ring` (`*storage.ScanRing`) | Zero synchronisation (`storage/scan_ring.go:23-37`) — plain fields |
| `prefetchedThru` | Prefetch watermark, meaningless across workers |
| `statReturned` | Per-worker count, summed at Close |

The `sctx` reset (`:1727-1731`) stays exactly where it is — at *that worker's*
block boundary. Since each worker owns its arena and its pages, the reset
cadence is unchanged from serial execution. This is the precise reason the
[03](03-concurrency-substrate.md) §3 contract exists: the arena reset is
per-worker, so a row that escaped to the leader unpromoted would be invalidated
by a reset the leader cannot see.

## 4. Behaviours that need re-tuning, not re-designing

### 4.1 Ring buffer activation

The `ScanRing` strategy activates when `nBlocks > pool.Capacity()/4`
(`operators_storage.go:772`) and gives the scan private buffers so a large scan
does not evict the whole pool.

Under N workers this heuristic is wrong in both directions: N rings consume N
times the private buffers, and each worker sees the same `nBlocks` so all N
activate together. The rule should become a function of the *per-worker* share
— activate when `nBlocks/nWorkers > pool.Capacity()/(4*nWorkers)`, which
reduces to the same test, but with each ring sized for its share rather than
the whole relation.

Stated as a re-tuning item rather than a solved design: the correct ring size
under parallelism is an empirical question, and [09](09-verification-and-measurement.md)
requires measuring pool hit rate before and after. The safe default for the
first implementation is to **disable the ring for parallel scans** and rely on
the pool, then re-enable it once measured.

**That default has two non-obvious side effects, both of which must be
deliberate rather than discovered:**

1. **It turns hint-bit writing on for exactly these scans.** The hint-bit path
   is gated on `needsXminHintBit := o.pinned != nil && …`
   (`operators_storage.go:1526`), and under the ring `o.pinned` is nil. So
   large-relation scans that previously never wrote hint bits will start doing
   so the moment they go parallel. The writes are correctly latched (§4.3) and
   PG-consistent, but the change in *behaviour* is larger than "we turned off a
   buffering strategy" suggests.
2. **It removes pool-pollution protection at the worst possible moment.** The
   ring exists precisely because a scan larger than `Capacity()/4` would evict
   the pool; disabling it means N workers stream exactly such a relation
   straight through the shared pool.

If the ring is later re-enabled, note also that its cache-hit path holds
`slot.RLock()` across the whole page (`storage/scan_ring.go:65-69`) — the
lock-hold pattern M0100-0005 deliberately removed from the pool path
(`operators_storage.go:1424-1432`). Not parallel-specific, but it becomes more
consequential with N readers.

### 4.2 Prefetch

`prefetchedThru` keeps `seqScanLookahead` blocks ahead of `curBlock`. With a
shared allocator, a worker's *own* next block is no longer `curBlock+1` — it is
whatever the allocator hands out. Per-worker prefetch of the block it is about
to request is therefore wrong.

Options: (a) drop prefetch for parallel scans (simplest, and N workers already
provide natural I/O concurrency); (b) have the allocator prefetch ahead of the
global cursor. **v1 chooses (a)**: N concurrent workers each doing a synchronous
page read already produce the I/O parallelism prefetch was emulating, and (b)
adds a shared-state write on the allocation path for a benefit that is
speculative until measured.

### 4.3 Hint bits are permitted

The scan sets `HeapXminCommitted` hint bits (`:1683-1691`) under the page's
content latch with `Pool.MarkDirtyHintBit`. This is a write on the read path
and it is **allowed** — correctly latched, and PG permits parallel workers to
do exactly this. Restated here because a reviewer applying
[03](03-concurrency-substrate.md) §7's read-only rule mechanically would flag
it. The self-XID guard at `:1526-1529` is unaffected by parallelism.

## 5. Statistics

`recordRelScan` (`:1383`) reports per-scan counts at Close. Each worker
reports its own; the totals are correct by summation.

If contention on the stats path shows up, `internal/stats/counter.go`'s per-P
sharded `Counter` (with `runtimeshim.PinP`) is the ready-made remedy — designed
in [`perf-optimize/08-runtime-internals.md`](../perf-optimize/08-runtime-internals.md)
§4 for exactly this. Not adopted pre-emptively.

## 6. EXPLAIN

The node renders as `Parallel Seq Scan on <table>`, matching PG.

One wrinkle: goopg appends a goopg-invented `(stats)` suffix when the table has
ANALYZE statistics (`internal/executor/operators_explain.go:1109,1112`),
producing `Seq Scan on lineitem (stats)`. Composing that with a `Parallel `
prefix yields `Parallel Seq Scan on lineitem (stats)`, which is PG's label plus
a non-PG suffix. That is consistent with existing behaviour and is the right
choice — but it means plan-gate snapshots will not be byte-comparable to PG's
output for these nodes, which was already true. Noted so it is a decision
rather than a surprise.

## 7. Divergence from PostgreSQL

| PG | goopg | Rationale |
| --- | --- | --- |
| `ParallelBlockTableScanDesc` in DSM, spinlock-protected, carrying chunk state and sync-scan position | One `atomic.Uint64` in the heap | goopg has no sync-scan and no chunking (§2.1), so the state reduces to a counter |
| Chunked block allocation with ramp-down | One block per allocation | The atomic is cheap enough that chunking's motivation disappears |
| Workers each attach to the DSM scan descriptor | Workers hold a pointer | No transport layer needed |
| `posix_fadvise`-style prefetch coordinated per worker | Prefetch disabled for parallel scans in v1 | N workers supply the I/O concurrency directly (§4.2) |

The whole node is, in goopg, roughly "replace an `int` cursor with an
`atomic.Uint64` and make sure nothing else is shared". The engineering risk is
concentrated entirely in *what stays per-worker* (§3), not in the allocator.
