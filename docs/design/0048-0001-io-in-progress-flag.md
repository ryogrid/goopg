# 0048-0001 — `BM_IO_IN_PROGRESS` atomic flag

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0048 — Buffer pool concurrency hardening
**Supersedes:** —

## Context

When two backends miss on the same page concurrently, both today enter
the smgr read path. The result is correct (both return the same bytes)
but wasteful: extra disk I/O, extra buffer-pool churn (the second read
needs another victim), and a real wall-time penalty under heavy
concurrency.

Upstream marks the buffer descriptor `BM_IO_IN_PROGRESS` before issuing
the read; concurrent backends spotting the flag wait on a CV. Only one
read happens.

## Plan

1. `internal/storage/bufpool.go::BufferDesc`:
   - Move existing `state` byte to a `uint32` if not already (need atomic
     ops on multiple flag bits).
   - Add bit constants: `BM_IO_IN_PROGRESS`, `BM_IO_ERROR`, `BM_VALID`.
   - Add a per-descriptor wait channel (or shared sync.Cond bound to a
     mutex partition).
2. `BufferAlloc` (or whatever today's miss path is named):
   - On miss, atomically claim the descriptor and CAS `BM_IO_IN_PROGRESS`
     to set; backends that lose the CAS go down the wait path.
   - Issue the read (existing smgr path).
   - On success, atomically clear `BM_IO_IN_PROGRESS`, set `BM_VALID`,
     and broadcast the wait channel.
   - On error, set `BM_IO_ERROR` + clear `BM_IO_IN_PROGRESS`, broadcast,
     and have the original requester surface the error to the caller.
     Waiters retry.
3. New wait-event class `BufferIO` with sub-events `BufferRead`,
   `BufferWrite` (M0024 wait-event taxonomy).
4. The same flag bit guards the *write-out* path — concurrent eviction
   attempts on the same dirty buffer are serialised.

## Definition of Done

- Stress test: 64 goroutines pin the same cold page; assert `smgr.Read`
  invocation count = 1.
- Same test with the dirty-eviction pattern: only one writeback.
- M0022 `pg_stat_activity` exposes `BufferRead` / `BufferWrite` waits
  during the contention window.
- No regression: pgbench TPS ≥ post-M0042 baseline.

## Upstream reference

- `postgres/src/backend/storage/buffer/bufmgr.c` — `BufferAlloc`,
  `StartBufferIO`, `TerminateBufferIO`, `WaitIO`.
- `postgres/src/include/storage/buf_internals.h` — `BM_IO_IN_PROGRESS`.

## goopg references

- `internal/storage/bufpool.go`, `internal/storage/smgr.go`.
- `docs/design/root-0005-buffer-manager.md`,
  `0032-0001-heap-arena-replacement.md`.
- `docs/design/0024-0001-...` (wait-event taxonomy).
