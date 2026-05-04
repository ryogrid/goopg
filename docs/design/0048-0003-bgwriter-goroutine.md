# 0048-0003 — Dedicated bgwriter goroutine

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0048 — Buffer pool concurrency hardening
**Supersedes:** —

## Context

Foreground victim search occasionally lands on a dirty buffer; today
that backend writes the buffer out itself before reusing the slot. The
write happens on the critical path of a query and shows up in p99
latency.

Upstream runs a *background writer* that scans the LRU on a timer,
writes out a small batch of dirty buffers each tick, and resets the
"recently-used" bit. Foreground victim search almost always finds a
clean slot.

## Plan

1. New goroutine `internal/storage/bgwriter.go::Run(ctx)`:
   - Tick every `bgwriter_delay` (GUC, default 200ms).
   - Walk the clock-sweep cursor; for each entry whose
     `(usage_count == 0 && dirty == true)` write it out via the smgr
     write path; clear the dirty bit; do not unpin (only the original
     pinner can do that).
   - Cap per-tick writes at `bgwriter_lru_maxpages` (GUC, default 100).
2. The bgwriter does **not** call `fsync` — durability is the
   checkpointer's job. Mirrors upstream and the M0042 separation of
   concerns.
3. New stats counters surfaced via `pg_stat_bgwriter` (M0022): buffers
   cleaned, max-written-clean events, halt_event count.
4. Wire-in: dispatcher starts the bgwriter on server-start and stops
   it during graceful shutdown (after the last user backend, before the
   final checkpoint).
5. Foreground `GetBuffer` interaction: when the foreground hits a dirty
   victim, it still writes (correctness must not depend on the bgwriter
   being scheduled). A counter tracks how often the foreground had to
   write — target ≤ 5% on the pgbench mixed workload.

## Definition of Done

- Bgwriter goroutine present; clock-sweep cursor advances on the
  expected cadence under a steady write load.
- Foreground writes count ≤ 5% on the pgbench mixed workload.
- pg_stat_bgwriter counters exposed.
- Graceful shutdown: bgwriter exits cleanly; final checkpoint catches
  any remaining dirty buffers.

## Upstream reference

- `postgres/src/backend/postmaster/bgwriter.c` —
  `BackgroundWriterMain`, `BgBufferSync`.
- `postgres/src/backend/storage/buffer/bufmgr.c` —
  `SyncOneBuffer`, the call shared between bgwriter and checkpointer.

## goopg references

- `internal/storage/bufpool.go` — clock-sweep cursor.
- `internal/storage/checkpointer.go` — distinct goroutine; this doc
  formalises the "do not also do bgwriter's job" boundary.
- `docs/design/0042-...` — WAL writer / bgwriter / checkpointer
  separation.
