# 0046-0001 — Heap-Only Tuples (HOT)

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0046 — Heap & MVCC maturation
**Supersedes:** —

## Context

`docs/reference/ref-007-heap-mvcc.md` notes that goopg's `heap_update` always
inserts a fresh tuple **and** writes a new index entry on every index, even
when the UPDATE did not touch any indexed column. On UPDATE-heavy workloads
this is the single biggest source of index bloat: a 100-million-row table
with one secondary index can grow that index by gigabytes per day even
though the indexed key is stable.

Upstream solves this with HOT — a new tuple version is chained on the same
heap page via `t_ctid`, the redirect carries `HEAP_HOT_UPDATED` /
`HEAP_ONLY_TUPLE` flags, and **no index entries are written**. The next
`heap_page_prune_opt` collapses dead links in the chain. Index lookups
that land on a redirect entry follow the chain through `t_ctid`.

## Plan

1. Detect "no indexed column changed". `internal/access/heap` gains
   `HeapDetermineModifiedColumns(rel, old, new)`; `internal/catalog`
   exposes the indexed-column bitmap per relation.
2. New tuple-header bits `HEAP_HOT_UPDATED`, `HEAP_ONLY_TUPLE` in
   `internal/access/heap` (mirroring upstream constants exactly).
3. Modify `heap_update` to:
   - Try same-page insert first when (a) no indexed column changed and
     (b) free space is available on the page hosting the old tuple.
   - On success: stamp `t_ctid` on old tuple to point to the new
     line-pointer slot, set `HEAP_HOT_UPDATED` on old, `HEAP_ONLY_TUPLE`
     on new, **skip the index-insert loop**.
   - Fall back to the existing cross-page update when either condition
     fails.
4. Index-fetch path (`internal/access/btree/scan.go` / index-scan operator)
   walks the HOT chain when the heap entry it lands on is marked
   `HEAP_HOT_UPDATED`.
5. WAL: a HOT update emits a `HEAP_UPDATE` record with the new
   `XLH_HOT_UPDATE` flag bit. Recovery applies the same same-page-insert +
   redirect-stamp idempotently (page LSN gates re-application — depends on
   M0017 / M0045's pd_lsn discipline).
6. VACUUM (`internal/storage/vacuum.go`) recognises HOT chains during the
   prune phase and reclaims the entire chain when its tail is dead.

## Definition of Done

- HOT same-page update path lands behind a feature flag GUC
  (`enable_hot_updates`, default on).
- pgbench-style UPDATE workload: index size growth ≤ 10% of pre-HOT
  baseline at 100k transactions.
- Existing index-fetch tests still green; new test exercising HOT-chain
  follow-through is added.
- WAL replay round-trip test: HOT update record applied to a clone yields
  byte-identical page state.

## Upstream reference

- `postgres/src/backend/access/heap/heapam.c` — `heap_update`,
  `HeapDetermineModifiedColumns`.
- `postgres/src/backend/access/heap/pruneheap.c` —
  `heap_page_prune_opt` (consumed by 0046-0002).
- `postgres/src/include/access/htup_details.h` — `HEAP_HOT_UPDATED`,
  `HEAP_ONLY_TUPLE`, `HEAP_UPDATED`.

## goopg references

- `internal/access/heap/heapam.go` — current `Update` path.
- `docs/design/root-0006-storage-format.md` — tuple-header layout.
- `docs/design/root-0007-mvcc-and-snapshots.md` — visibility invariants.
