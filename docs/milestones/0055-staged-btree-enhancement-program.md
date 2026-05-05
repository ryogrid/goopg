# Milestone 0055 — Staged B-tree Enhancement Program

**Status:** planned
**Depends on:** Milestone 0002 (concurrent B-tree), Milestone 0047 (bulk load/page deletion/dedup foundation), Milestone 0054 (TPC-H perf follow-through)
**Drives:** Production-grade B-tree write scalability, long-run space stability, and predictable large-index build behavior under realistic OLTP/HTAP pressure.

## Context

The following two reports identify concrete simplifications and bottlenecks in the current B-tree implementation and provide an upstream-aligned improvement direction:

- analysis/btree-simplifications-and-performance-upgrade-plan-2026-05-05.md
- analysis/btree-goopg-vs-postgres-reference-map-2026-05-05.md

The current implementation is functionally solid for many scenarios but still has staged simplifications around:

1. split-path write serialization and incomplete split lifecycle
2. whole-page rewrite insertion and count-based split point selection
3. dedup effectiveness drift after incremental inserts
4. vacuum deletion/recycling protocol maturity
5. CREATE INDEX memory scaling (all-in-memory collect + sort)

Milestone 0055 converts those findings into an implementation sequence with measurable acceptance gates.

## Scope

### Phase A — Write-path CPU and split-efficiency baseline

- Replace full-page rewrite insertion with in-page binary-position insert where possible.
- Introduce byte-aware split-loc policy (instead of count-midpoint only).
- Add rightmost-leaf insert fastpath cache for append-shaped workloads.

### Phase B — Keep dedup effective after initial build

- Add in-place posting-list growth/merge for duplicate-key incremental inserts.
- Add optional local dedup compaction before split on duplicate-heavy pages.

### Phase C — Multi-writer structural protocol

- Introduce explicit incomplete-split state and write-side split completion routine.
- Replace splitMu structural serialization with lock-order-safe multi-writer split flow.
- Restore full sibling-link invariant maintenance (including Prev updates).

### Phase D — Deletion and page recycling maturity

- Move from simplified empty-leaf unlink to two-phase deletion protocol.
- Add WAL records for deletion phases and replay-safe continuation.
- Recycle safely deletable pages to reusable free space management.

### Phase E — Large CREATE INDEX scalability

- Replace all-in-memory collect/sort with spill-capable external spool/sort.
- Move uniqueness checks to sorted-stream adjacency checks.

## Required Design Docs

- docs/design/0055-0001-btree-write-path-and-steady-state-dedup.md
- docs/design/0055-0002-btree-multi-writer-split-protocol.md
- docs/design/0055-0003-btree-page-deletion-and-recycling-protocol.md
- docs/design/0055-0004-btree-external-sort-build-and-uniqueness.md

## Definition of Done

1. Write-path overhead:
   - insert-path profiles show no whole-page decode/rewrite hotspot as a top driver under representative insert workload.
   - split churn is reduced on variable-width keys via byte-aware split-loc (measured by split count and p95 latency).
2. Dedup persistence:
   - duplicate-heavy indexes retain compactness under sustained incremental insert workload (bounded drift vs post-build baseline).
3. Concurrency:
   - splitMu is removed from the structural write path (or strictly relegated to temporary compatibility guardrails not used in steady-state inserts).
   - multi-writer insert stress test passes without lost/duplicate entries and without deadlock.
4. Deletion/recycling:
   - two-phase deletion protocol is replay-safe and validated by crash/restart tests.
   - deleted pages are recycled and reused in subsequent index growth scenarios.
5. Build scalability:
   - CREATE INDEX peak memory is bounded by configured sort/work budget on large datasets.
   - uniqueness checks no longer require table-scale hash state.
6. End-to-end benchmarks and report:
   - publish one analysis report under analysis/ with before/after metrics (TPS/latency, index size, build RSS/time, profile deltas).

## Out of Scope

- New index access methods (GiST/SP-GiST/GIN/BRIN).
- Locale-aware collation rework beyond current bytewise ordering model.
- Full CREATE INDEX CONCURRENTLY semantics.

## Reference

- analysis/btree-simplifications-and-performance-upgrade-plan-2026-05-05.md
- analysis/btree-goopg-vs-postgres-reference-map-2026-05-05.md
- postgres/src/backend/access/nbtree/README
- postgres/src/backend/access/nbtree/nbtinsert.c
- postgres/src/backend/access/nbtree/nbtsearch.c
- postgres/src/backend/access/nbtree/nbtpage.c
- postgres/src/backend/access/nbtree/nbtdedup.c