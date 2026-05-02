# Milestone 0032 — shared_buffers Large-Value Crash Fix (madvise on Eviction)

**Status:** planned
**Depends on:** Milestone 0029 (HammerDB TPC-H run)
**Drives:** Enable `shared_buffers=2000M` (or larger) without OOM during TPC-H data load, so heap and index pages fit entirely in the buffer pool for query performance.

## Context

### Problem

Setting `shared_buffers=2000M` causes the goopg process to be OOM-killed during TPC-H
data load (schema build with HammerDB). The same workload succeeds with
`shared_buffers=256M`. The root cause was identified in
`analysis/memory-leak-investigation-report.md` §6:

1. The buffer pool arena is mmap'd with `MAP_PRIVATE|MAP_ANONYMOUS` at the configured
   size (2.0 GB for 2000M → 256,000 slots × 8 KiB).
2. At startup the virtual address space is reserved but physical pages are NOT allocated
   (RSS ≈ 36 MB).
3. As pages are touched (Pin/eviction cycles during COPY load), the kernel's demand
   paging fills in physical pages.
4. **Once a physical page becomes resident, it STAYS resident forever** — eviction
   reuses the slot but never tells the kernel the old physical page is no longer needed.
   The arena RSS monotonically grows to the full 2.0 GB.
5. Combined with Go heap peaks (temporary row buffers during COPY/joins) and kernel
   page cache, total RSS exceeds available system RAM → OOM killer.

This is structurally different from PostgreSQL's behaviour: upstream PG's
`shared_buffers` uses `shmget` (System V shared memory), where the kernel can swap
out pages under memory pressure. goopg's MAP_ANONYMOUS arena has no file backing, so
the kernel has nowhere to write dirty pages and no signal that clean pages are
disposable.

### Why this matters for TPC-H

With `shared_buffers=256M` (the current workaround), a TPC-H SF=1 dataset (~1.5 GB of
heap data + indexes) cannot fit in the buffer pool. This forces frequent page
replacements (reads from disk for every accessed block), making query execution
extremely slow. Q14 took 401s on a 256MB pool. With the full dataset in memory (2.0 GB
pool), queries should run in seconds or low tens of seconds — the TPC-H workload's
natural working set fits in 2 GB.

### Proposed Fix

Call `madvise(MADV_DONTNEED)` on the evicted slot's arena region after it is flushed
and removed from `byTag`. This tells the kernel to discard the physical pages backing
that 8 KiB region. The virtual mapping remains valid; the next Pin that reads from
disk or calls InitPage will demand-page fresh zero-filled pages.

This mirrors the technique upstream PostgreSQL uses internally for
`PG_BUFFER_CACHE_EVICT` (Linux's `madvise(MADV_DONTNEED)` on freed buffer cache
regions), and is the standard way to let the kernel reclaim anonymous-mmap'd pages
that are no longer in use.

### Startup validation (secondary)

Add a startup check: if `shared_buffers` exceeds a fraction of system available memory
(e.g., 50% of `MemAvailable` from `/proc/meminfo`), emit a WARNING log line but continue.
This gives the operator an early diagnostic before the late-OOM scenario develops.

## Required Design Docs

1. `docs/design/0032-0001-madvise-buffer-eviction.md` — Mechanism for advising the
   kernel on evicted buffer pages, integration points in Pin/PinNew/flushSlot, and
   RSS behaviour before/after.
2. `docs/design/0032-0002-shared-buffers-startup-validation.md` — Startup memory
   check: reading `/proc/meminfo`, comparing `shared_buffers` to available memory,
   warning emission via `slog`.

## Definition of Done

1. **madvise on eviction implemented**: After `flushSlot` writes a dirty page to disk
   and the slot is removed from `byTag`, the slot's arena region is advised with
   `MADV_DONTNEED` before the slot is reassigned to a new tag.

2. **No regression on existing tests**: The madvise is a no-op from the perspective of
   data correctness — it only affects RSS. All existing `go test ./...` passes.

3. **RSS reduction verified**: A manual test shows that with `shared_buffers=2000M`,
   the process RSS after a full TPC-H COPY load does NOT grow to the full arena size.
   Instead, RSS stabilizes at the working set of actively-used pages (typically much
   smaller than the full arena).

4. **TPC-H load succeeds at 2000M**: `bench/tpch/setup_goopg.sh` completes schema
   build + data load without OOM crash with `shared_buffers=2000M`.

5. **TPC-H power test speed improved**: Queries execute measurably faster than the
   256MB baseline (subjective — the working set now fits in the pool). At minimum, Q14
   completes in substantially less than the 401s baseline.

6. **Startup validation**: If `shared_buffers > 50% * MemAvailable`, a WARN log line
   is emitted: `event=shared_buffers_large_warning` with `shared_buffers_mb` and
   `mem_available_mb` fields.

7. **Design docs accepted**: Both `0032-0001-*.md` and `0032-0002-*.md` written and
   indexed in `docs/design/README.md`.

## Reference

- `internal/storage/arena.go:30-48` — mmap allocation (MAP_PRIVATE|MAP_ANONYMOUS)
- `internal/storage/bufpool.go:470-538` — PinNew (evict → flush → reuse)
- `internal/storage/bufpool.go:542-628` — Pin (evict → flush → reuse)
- `internal/storage/bufpool.go:924-970` — flushSlot, evictLocked (clock sweep)
- `internal/storage/bufpool.go:256-291` — NewPool (arena creation)
- `internal/config/defaults.go:120-132` — shared_buffers GUC, MaxVal=1EB
- `cmd/goopg/main.go:680-703` — poolSlotsFromGUC (KB→slots)
- `analysis/memory-leak-investigation-report.md` — root cause analysis
- `bench/tpch/env_goopg.sh:67-72` — current GOMEMLIMIT workaround
