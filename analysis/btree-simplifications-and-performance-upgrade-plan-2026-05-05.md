# goopg B-Tree Simplifications and High-Performance Upgrade Plan

Date: 2026-05-05

## Executive Summary

goopg's B-tree implementation has already moved beyond the original single-lock v0 design and now includes:

- Lehman-Yao style high-key + right-link recovery for readers
- split-path WAL hook support
- sort-then-build bulk create
- leaf posting-list deduplication during bulk create
- index vacuum entry pruning and empty-leaf unlinking

However, several parts are still intentionally simplified compared to PostgreSQL nbtree, especially around write concurrency, split/deletion protocol, and maintenance-path sophistication. These simplifications are correct enough for current workloads, but they leave measurable performance headroom and future correctness risk under heavier concurrent write + vacuum pressure.

This report identifies those simplifications and proposes a staged, high-performance implementation plan, using PostgreSQL code in postgres/src/backend/access/nbtree as the oracle.

## Evidence Base (goopg)

Primary code and design anchors reviewed:

- internal/access/btree/btree.go
- internal/access/btree/btree_vacuum.go
- internal/access/btree/bulkload.go
- internal/access/btree/posting.go
- internal/executor/operators_ddl.go
- docs/design/0002-0002-btree-concurrency.md
- docs/design/0047-0001-btree-bulk-load.md
- docs/design/0047-0002-page-deletion.md
- docs/design/0047-0003-deduplication.md

## Oracle Base (PostgreSQL nbtree)

Primary upstream references reviewed:

- postgres/src/backend/access/nbtree/README
- postgres/src/backend/access/nbtree/nbtinsert.c
- postgres/src/backend/access/nbtree/nbtsearch.c
- postgres/src/backend/access/nbtree/nbtpage.c
- postgres/src/backend/access/nbtree/nbtdedup.c

Notable upstream mechanisms used as targets:

- _bt_moveright and split completion path
- INCOMPLETE_SPLIT handling
- rightmost-leaf insert fastpath cache
- binary-search insert positioning (_bt_binsrch_insert)
- two-phase page deletion (_bt_pagedel / _bt_unlink_halfdead_page)
- deleted-page recycling into FSM
- in-place dedup pass during insert pressure

## Current Simplifications in goopg

## 1) Structural write serialization around splits

Current state:

- goopg allows no-split inserts to proceed on leaf latches.
- Structural changes are serialized by splitMu in BTree (internal/access/btree/btree.go).

Simplification:

- split-path concurrency is intentionally reduced to a single structural writer.

Performance implication:

- write scaling under split-heavy workloads plateaus early.
- hot key ranges with frequent splits become a serialization point.

Upstream contrast:

- PostgreSQL uses write-coupled protocols with incomplete-split handling rather than a single tree-level split mutex.

## 2) No INCOMPLETE_SPLIT lifecycle and no on-the-fly split completion

Current state:

- goopg supports right-link recovery for readers, and split atomicity for left+right pages with LogSplit hook.
- Parent insertion can still be deferred from child split in crash windows (documented).

Simplification:

- No explicit INCOMPLETE_SPLIT page state and no _bt_finish_split equivalent during write descent.

Performance and robustness implication:

- current model is acceptable for reader correctness, but leaves a maintenance debt for write-side recovery from partially propagated structure.

Upstream contrast:

- PostgreSQL marks incomplete split and completes it during writer traversal before allowing further insert on affected pages.

## 3) Prev-link maintenance intentionally skipped in split protocol

Current state:

- design doc explicitly notes that updating old right sibling Prev is omitted in this stage.

Simplification:

- only forward Next chain is treated as authoritative for readers.

Performance/maintenance implication:

- backward/bi-directional traversal and deletion bookkeeping become harder or more defensive.
- page deletion logic must compensate for weaker sibling invariants.

Upstream contrast:

- PostgreSQL maintains stronger sibling-link invariants and validates both sides during page deletion.

## 4) Insert path still rewrites whole page per insertion

Current state:

- insertItemSorted reads all items, appends new item in sorted slice, resets page, and rewrites all entries.
- comment in code explicitly calls this quadratic and acceptable only while pages are small.

Simplification:

- no in-page slot shift / offset-array insertion.

Performance implication:

- extra CPU and memory churn (decode + alloc + rewrite) per insert.
- write amplification increases with variable-length keys and dedup-expanded pages.

Upstream contrast:

- PostgreSQL performs binary-search insertion location and in-page placement without full page reconstruction.

## 5) Split location is count-based midpoint, not byte-aware split-loc

Current state:

- split point is mid := len(allItems)/2.

Simplification:

- ignores variable-size tuple layout and post-split free-space quality.

Performance implication:

- higher probability of poor fill distribution, more frequent follow-up splits, worse cache behavior.

Upstream contrast:

- PostgreSQL split location logic is byte/cost aware and interacts with dedup/single-value patterns.

## 6) Dedup is bulk-build oriented; incremental inserts decompact touched pages

Current state:

- deduplicateToRawItems is used in BulkCreate leaf build.
- pageItems expands posting items to individual (key,TID) entries.
- insertItemSorted writes regular items back.
- design doc already states compaction is lost on pages receiving later Insert.

Simplification:

- no in-place posting-list growth/merge in incremental insert path.

Performance implication:

- duplicate-heavy indexes gradually drift from compact representation after normal OLTP writes.
- index size and buffer pressure regress over time unless rebuilt.

Upstream contrast:

- PostgreSQL can execute dedup passes under insert pressure, not only during initial build.

## 7) HighKey length is capped to 32 bytes

Current state:

- MaxHighKeyLen is fixed at 32 and split fails when separator key exceeds this bound.
- tests for posting dedup intentionally keep encoded keys <= 32 bytes.

Simplification:

- high-key storage budget is constrained and not generalized to long varchar/composite separators.

Performance/correctness implication:

- long-key workloads can fail on split with separator length errors.
- key-shape-dependent behavior can cause unpredictable operational limits.

Upstream contrast:

- PostgreSQL stores pivot/high-key tuples in normal page item format with no fixed 32-byte separator cap.

## 8) Bulk build is fully in-memory (entries slice + uniqueness map)

Current state:

- create index path collects all entries in memory before BulkCreate.

Simplification:

- no external spool/sort pipeline, no memory throttling, no run generation.

Performance implication:

- memory pressure grows with table size and duplicate checks.
- build scalability is constrained by RAM, not only by IO/CPU.

Upstream contrast:

- PostgreSQL uses spool + sort and can spill, keeping large index builds stable.

## 9) Vacuum page deletion is simpler than PostgreSQL two-phase protocol

Current state:

- goopg removes dead leaf entries, marks empty leaves deleted, unlinks siblings, removes parent downlink, and may reset tree to empty root.
- docs explicitly call out limitations: only leaf deletion, no full recursive internal cleanup, no dedicated unlink WAL records, no autovacuum integration.

Simplification:

- no half-dead page protocol with full lock-order discipline and recovery continuation semantics.
- resetToEmptyRoot can leave unreachable orphan blocks.

Performance implication:

- long-term space management and page reuse efficiency are weaker.
- maintenance cost and file bloat handling are less predictable under churn.

Upstream contrast:

- PostgreSQL uses staged deletion with WAL records and safely recycles deleted pages via FSM when visible horizons allow.

## 10) Missing rightmost-leaf insert fastpath cache

Current state:

- goopg always descends from root for inserts.

Simplification:

- no backend-local rightmost target block cache for append-like key patterns.

Performance implication:

- avoidable tree descent overhead for monotonic/near-monotonic insert workloads.

Upstream contrast:

- PostgreSQL _bt_search_insert uses rightmost leaf cache with conditional lock acquisition.

## High-Performance Upgrade Plan (Staged)

## Phase A: Remove highest write-path CPU overhead first

A1. Replace full-page rewrite insert with in-page binary insertion

- Add leaf/internal binary search over raw items for insertion offset.
- Shift line pointers and append tuple bytes without decode-all + rewrite-all.
- Preserve existing pageItems as slow path fallback for rare repair cases.

Expected impact:

- lower per-insert CPU and allocation pressure.
- immediate OLTP write latency reduction.

A2. Implement byte-aware split-loc selection

- Split by accumulated byte occupancy and target fillfactor, not by item count.
- Add single-value guardrails similar to PostgreSQL behavior to avoid pathological churn.

Expected impact:

- fewer re-splits, better space utilization, smoother latency tails.

A3. Add rightmost-leaf insert fastpath cache

- Maintain per-session candidate rightmost leaf block for append-friendly patterns.
- Use conditional lock attempt and fallback to full descent when invalid.

Expected impact:

- meaningful speedup on monotonic PK/timestamp inserts.

## Phase B: Preserve dedup in steady-state OLTP

B1. In-place posting-list growth/merge on duplicate-key insert

- On duplicate probe, prefer extending existing posting item when size budget allows.
- If full, split posting into bounded chunks while preserving key order.

B2. Opportunistic dedup pass under split pressure

- Before splitting duplicate-heavy leaf pages, run local dedup compaction pass.
- Reuse posting encoding primitives from posting.go.

Expected impact:

- sustained compactness beyond bulk build.
- lower index bloat and better buffer residency.

## Phase C: Complete write-side concurrency protocol

C1. Introduce explicit incomplete-split state and completion path

- Add page flag and split-completion routine for writers traversing affected pages.
- Ensure only one writer can finish each incomplete split branch safely.

C2. Replace splitMu with lock-order-safe multi-writer split protocol

- Enforce deterministic lock ordering (right, then up) for sibling/parent interactions.
- Keep reader move-right semantics unchanged.

C3. Restore and maintain Prev-link invariants during split

- Update old right sibling Prev as part of split critical section.
- Add defensive validation in maintenance paths.

Expected impact:

- better writer scaling under contention.
- cleaner page-deletion and traversal invariants.

## Phase D: Upgrade vacuum deletion and space recycling

D1. Move to two-phase deletion protocol

- Introduce half-dead semantics and explicit unlink stage.
- Add WAL record shape for mark-half-dead and unlink actions.

D2. Recycle safely deleted pages into free space management

- Track safety horizon and return pages to reusable pool/FSM equivalent.
- Reduce file growth and improve long-run stability under churn.

D3. Integrate index vacuum with autovacuum path

- Use same deletion pipeline for background maintenance.

Expected impact:

- lower long-term bloat and fewer cold blocks.
- more PostgreSQL-like maintenance behavior.

## Phase E: Make CREATE INDEX scale beyond memory

E1. External spool + sort

- Replace all-in-memory entries slice with run-based sorter that can spill.
- Keep current bottom-up page builder as consumer of sorted stream.

E2. Streaming uniqueness check

- Verify uniqueness on sorted stream (adjacent equal keys), removing large hash set requirement.

Expected impact:

- predictable large-table build behavior.
- significantly reduced peak memory.

## Observability and Acceptance Criteria

Recommended benchmark gates per phase:

- pgbench -N/-c 1,4,16,32 write TPS and p95 latency
- split rate, average split depth propagation
- index size growth under update/delete churn (before/after vacuum)
- duplicate-heavy index size drift over sustained insert workload
- create index peak RSS and elapsed time on large tables

Concrete target examples:

- write TPS should scale at least 2x from c=1 to c=16 on non-fsync-bound setups after Phase A+C.
- duplicate-heavy index size drift after 10M inserts should stay within 20% of post-build baseline after Phase B.
- create index peak memory should be bounded by configured work memory after Phase E.

## Suggested Implementation Order

1. Phase A (highest ROI, low semantic risk)
2. Phase B (retains current space wins under OLTP)
3. Phase E (operational scalability for big builds)
4. Phase C (hard but necessary for full writer scaling)
5. Phase D (maintenance maturity and long-run bloat control)

This order maximizes near-term performance gains while deferring high-risk protocol work until cheaper wins are captured.

## Practical Notes for goopg

- Keep wire-compatible behavior for existing SQL-visible semantics; gate on-disk format changes by btree version bump and explicit reindex path.
- Add crash/recovery tests for each new multi-page atomic sequence before enabling wider writer concurrency.
- For each phase, add targeted pprof runs to verify that expected hotspots actually move (avoid protocol complexity without measurable payoff).

## Final Recommendation

Use PostgreSQL nbtree behavior as a protocol oracle, but port in thin slices with performance gates. The fastest path to meaningful gains is:

- first reduce per-insert CPU and split inefficiency,
- then preserve dedup in steady-state writes,
- then complete multi-writer and deletion protocols.

That sequencing gives goopg a high probability of substantial OLTP improvement without taking on all nbtree complexity at once.