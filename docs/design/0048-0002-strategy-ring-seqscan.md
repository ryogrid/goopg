# 0048-0002 — Sequential-scan strategy ring

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0048 — Buffer pool concurrency hardening
**Supersedes:** —

## Context

A SeqScan over a 100M-row table walks 100k+ pages. Today each miss takes
a buffer from the global pool's clock-sweep, evicting whatever was in
that slot. The result: an analytical query that runs once a minute can
nuke the OLTP working set.

Upstream allocates a small per-scan "ring" of buffers (32 by default for
`BAS_BULKREAD`). The SeqScan operator only evicts from this ring;
foreground OLTP pages stay hot.

## Plan

1. New type `BufferAccessStrategy` in `internal/storage/strategy.go`:
   ```go
   type BufferAccessStrategy interface {
       NextVictim() *BufferDesc  // returns a ring slot, advancing internal cursor
       Release()                  // returned to the pool when scan ends
   }
   ```
   With concrete implementations `bulkReadStrategy` (32 slots) and
   `nilStrategy` (always returns nil, falls back to global clock-sweep).
2. `bufpool.GetBuffer(rel, blk, strategy)` — extended signature; when
   `strategy != nil` and the requested page is not already in the pool,
   the victim comes from the strategy's ring instead of clock-sweep.
3. Executor wiring:
   - SeqScan operator: when the planner has marked the scan
     `bulkRead = true`, allocate `bulkReadStrategy(32)` at Open and
     `Release()` at Close.
   - Bulk-build (0047-0001) does the same.
4. Planner heuristic: mark a scan `bulkRead = true` when the relation's
   estimated page count > `shared_buffers / 4`. Override-able via a
   plan hint in EXPLAIN later.
5. Ring slots are *not* dirty-write-suppressed — if the eviction target
   is dirty, the bgwriter (0048-0003) flushes it; the SeqScan operator
   just rotates to the next slot in the meantime.

## Definition of Done

- Test scenario: warm a 1k-buffer pool with hot pages, run a SeqScan
  over a relation with 5k pages, then verify ≥ 95% of the pre-existing
  hot pages are still in the pool.
- TPC-H integration suite still 22/22.
- No regression on pgbench small-data workloads.

## Upstream reference

- `postgres/src/backend/storage/buffer/freelist.c` —
  `GetAccessStrategy`, `BufferAccessStrategy` opaque struct.
- `postgres/src/backend/access/heap/heapam.c` — heap SeqScan
  uses `GetAccessStrategy(BAS_BULKREAD)`.

## goopg references

- `internal/storage/bufpool.go`, `internal/executor/seqscan.go`.
- `docs/design/root-0012-executor.md` — current SeqScan structure.
