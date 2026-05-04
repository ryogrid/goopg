# 0046-0003 — Free Space Map (FSM)

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0046 — Heap & MVCC maturation
**Supersedes:** —

## Context

goopg's `heapInsert` always extends the relation when the last-cached
target page can't fit the row. A workload with INSERT/DELETE churn ends up
with many half-empty pages but never reuses them. Upstream solves this
with the FSM: a per-relation tree-of-pages stored in a separate fork
(`<relfilenode>_fsm`) that summarises free-bytes per heap page; insert
target selection consults the FSM before extending.

## Plan

1. New fork constant `INIT_FORKNUM_FSM` in `internal/storage/smgr.go` /
   the RelFile fork enum.
2. `internal/storage/freespace/` package: tree-of-pages summarisation,
   leaf-page format (one byte per heap page indicating power-of-two
   free bytes — mirror upstream's bucket).
3. `FreeSpace.GetPageWithFreeSpace(rel, neededBytes) BlockNumber` returns
   the first page whose summarised free-bytes ≥ needed; falls back to
   `InvalidBlockNumber` (caller extends).
4. `FreeSpace.RecordPageWithFreeSpace(rel, blk, free)` updates the leaf
   bucket, propagates upward when a higher bucket changes.
5. Wire-in points:
   - `heapInsert` — replace "use cached target page" with
     `GetPageWithFreeSpace`.
   - `heapVacuum` (after prune / after page truncation) — call
     `RecordPageWithFreeSpace` with the new free byte count.
   - `heap_page_prune_opt` — likewise after a successful prune.
6. WAL: piggy-back on existing heap WAL records — FSM updates are
   non-critical (correct values are eventually rebuilt from data pages),
   so they go through `MarkDirty` only and are flushed by the bgwriter,
   not transaction-synchronously.

## Definition of Done

- New FSM fork created on first heap-relation init / on first insert if
  missing.
- `heapInsert` consults FSM and reuses non-tail pages when they have
  free space.
- 100k INSERT + 50k DELETE + VACUUM yields a single-page heap (regression
  test).
- Crash + restart with stale FSM still produces correct (if conservative)
  insert decisions; rebuild path runs lazily.

## Upstream reference

- `postgres/src/backend/storage/freespace/freespace.c`,
  `fsmpage.c` — tree, summarisation, lookup.
- `postgres/src/include/storage/freespace.h` — public API.

## goopg references

- `internal/storage/smgr.go` — fork enumeration.
- `internal/access/heap/heapam.go` — insert target selection.
- 0046-0002 — prune updates FSM after reclaim.
