# 0046-0002 — Opportunistic page pruning

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0046 — Heap & MVCC maturation
**Supersedes:** —

## Context

goopg currently reclaims dead heap-tuple slots only when VACUUM runs. A
page that sees a tight UPDATE loop within a single backend grows until it
fills up and the next UPDATE is forced to a new page even though every
prior version on the original page is invisible to every active snapshot.
Upstream avoids this with `heap_page_prune_opt`: each *read* of the page
that finds tuples older than the snapshot manager's `OldestXmin` runs an
in-place prune.

This doc specifies the goopg port. It depends on the snapshot manager's
existing `OldestXmin` field and the buffer-pool's pin-and-exclusive-latch
contract.

## Plan

1. `internal/access/heap/prune.go` — new file with `PagePruneOpt(buf,
   snap)` and `pagePrune(page, oldestXmin)`. The opt variant fast-paths the
   common case where the page does not have `PD_HAS_FREE_LINES` or any
   tuple older than `OldestXmin`.
2. Wire-in point: the buffer-pool `Pin` path (or the heap scan operator,
   whichever takes the exclusive latch first). Mirror upstream — prune
   only when `pd_prune_xid < OldestXmin` and the page is currently
   exclusively pinnable. If another backend holds shared pins, skip.
3. `pagePrune` walks the line-pointer array, classifies each tuple
   (`DEAD`, `RECENTLY_DEAD`, `LIVE`, `INSERT_IN_PROGRESS`,
   `DELETE_IN_PROGRESS`), redirects HOT chains in place, and packs the
   page. Item-pointer offsets stay stable for `LIVE` tuples
   (line-pointers are repointed; tuple bytes can move).
4. WAL: `XLOG_HEAP2_PRUNE` record per prune (mirror upstream). Recovery
   applies the same redirect / kill / freeze decision idempotently.
5. Stats: increment a per-relation `n_tup_dead_after_prune` counter
   surfaced in `pg_stat_all_tables` (M0022).

## Definition of Done

- `pagePrune` lands; opportunistic call site in the buffer-pin path is
  active by default behind `enable_opportunistic_prune` (default on).
- Tight UPDATE loop in a single transaction does not grow the page beyond
  a steady-state size.
- VACUUM no longer needs to reclaim HOT-only chains in the common case
  (regression test asserts a clean page after the prune-opt path runs).
- WAL replay test for `XLOG_HEAP2_PRUNE` round-trip.

## Upstream reference

- `postgres/src/backend/access/heap/pruneheap.c` —
  `heap_page_prune_opt`, `heap_page_prune`.
- `postgres/src/backend/access/heap/heapam_xlog.h` — `xl_heap_prune` shape.

## goopg references

- `internal/storage/bufpool.go` — pin-with-prune-callout site.
- `docs/design/root-0006-storage-format.md`, `root-0016-vacuum-and-analyze.md`.
- 0046-0001 (HOT chains the prune resolves).
