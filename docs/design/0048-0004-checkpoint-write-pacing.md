# 0048-0004 — Checkpoint write pacing

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0048 — Buffer pool concurrency hardening
**Supersedes:** —

## Context

`internal/storage/checkpointer.go` flushes every dirty buffer back to
back. On a busy workload that's enough to saturate sequential write
bandwidth for a few seconds; foreground latency spikes accordingly.

Upstream paces the dirty-buffer flush over `checkpoint_completion_target
× checkpoint_timeout` seconds. Default `target = 0.9` and
`timeout = 5min` → spread over 4.5 minutes. Foreground impact is barely
perceptible.

## Plan

1. New GUC `checkpoint_completion_target` (real, range [0.0, 1.0],
   default 0.9). Already declared in `0002-0001-checkpointing.md`'s
   plan but not yet implemented.
2. Compute pacing budget at checkpoint start:
   `budgetSeconds = target * checkpointInterval`.
3. Sort the dirty-buffer list by `(tablespace, relation, block)` for
   sequential I/O.
4. Walk the list with an inter-batch sleep:
   - Flush a batch of `checkpoint_flush_after` (≈ 32) buffers.
   - Sleep `budgetSeconds * batchSize / totalDirtyBuffers` between
     batches.
   - Skip sleep when the next checkpoint trigger fires (segment-driven
     `max_wal_size` over-shoot) — switch to flush-as-fast-as-possible.
5. Preserve the M0026 / M0042 WAL-before-data invariant: each buffer
   write still gates on the LSN watermark.
6. Stats: per-checkpoint `write_time`, `sync_time`, `total_time` exposed
   via `pg_stat_bgwriter` (M0022).

## Definition of Done

- Regression test: 200k dirty buffers checkpointed at `target = 0.5`
  with `interval = 30s` finishes between 14 s and 17 s.
- Foreground TPS impact during a paced checkpoint ≤ 20% (was ~50–70%
  on the synchronous-flush path).
- Segment-driven (`max_wal_size`) checkpoint trigger still completes
  fast (no sleep) — guards against unbounded WAL growth.

## Upstream reference

- `postgres/src/backend/postmaster/checkpointer.c` —
  `CheckpointWriteDelay`, `IsCheckpointOnSchedule`.
- `postgres/src/backend/storage/buffer/bufmgr.c` —
  `BufferSync` per-buffer sleep cadence.

## goopg references

- `internal/storage/checkpointer.go` — current synchronous flush.
- `docs/design/0002-0001-checkpointing.md` — declared GUC, deferred
  implementation.
- 0048-0003 (bgwriter cooperates by absorbing pre-checkpoint dirty
  pressure).
