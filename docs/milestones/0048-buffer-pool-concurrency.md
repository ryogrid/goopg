# Milestone 0048 — Buffer pool concurrency hardening

**Status:** planned
**Depends on:** Milestone 0032 (heap-arena buffer pool — landed), Milestone 0042 (PG-aligned buffered I/O — drops O_DIRECT so the bgwriter can rely on the kernel page cache), Milestone 0024 (wait-event taxonomy — new wait events plug into the existing infrastructure).
**Drives:** Eliminate the three remaining buffer-pool concurrency cliffs identified in `docs/reference/ref-003-buffer-pool.md`: duplicate I/O for the same page under contention, sequential-scan scribbling over hot pages, and synchronous all-buffers flush during checkpoint.

## 1. Context

After M0032 replaced the mmap arena with a Go-heap arena and M0042 retired O_DIRECT, the buffer pool's behaviour is correct but still has three concurrency / efficiency gaps relative to upstream:

1. **No `IO_IN_PROGRESS` flag.** Two concurrent backends that miss on the same page will both enter the I/O path. The second backend wastes one disk read and increases buffer-pool churn. Upstream marks the descriptor `BM_IO_IN_PROGRESS`, the second backend waits on a CV, and only one read happens.
2. **No sequential-scan strategy ring.** A SeqScan over a 100-million-row table walks the entire pool, evicting hot OLTP pages. Upstream allocates a 32-buffer "ring" that the SeqScan reuses; eviction stays inside the ring. goopg currently has no such concept; large analytical queries (TPC-H) systematically displace transactional working sets.
3. **Synchronous checkpoint flush.** Checkpointer calls `FlushAll` and writes every dirty buffer back to back; on a workload with a non-trivial dirty fraction this saturates write bandwidth for a few seconds and stalls foreground queries. Upstream pacing (`checkpoint_completion_target`) spreads the writeback over `checkpoint_timeout × completion_target` seconds. M0042 explicitly defers this to a follow-up; this is that follow-up.

A natural fourth piece — a dedicated background-writer goroutine — falls out of M0042's "WAL writer is the only goroutine that flushes WAL". The same separation applies to the buffer pool: a dedicated bgwriter ticks a clock-sweep slice on a timer and writes a bounded batch of dirty buffers, so foreground queries rarely see a synchronous victim flush.

## 2. Required Design Docs

1. `docs/design/0048-0001-io-in-progress-flag.md` — atomic `BM_IO_IN_PROGRESS` bit on the buffer descriptor with a CV / channel-based wait. New `BufferIO` wait-event class.
2. `docs/design/0048-0002-strategy-ring-seqscan.md` — `BufferAccessStrategy` shape (ring of 32 buffers; rotate-and-reuse), wired into the SeqScan operator and the bulk-load builder. Eviction prefers ring buffers before global pool.
3. `docs/design/0048-0003-bgwriter-goroutine.md` — dedicated bgwriter goroutine: clock-sweep slice on a timer, writes ≤ `bgwriter_lru_maxpages` dirty buffers per tick, never issues fsync. Foreground victim search rarely lands on dirty buffers.
4. `docs/design/0048-0004-checkpoint-write-pacing.md` — paced checkpointer writeback. `checkpoint_completion_target` GUC. Distribute the dirty-buffer flush over `target × interval` seconds with sleep-between-batches; preserve M0026's WAL-before-data invariant by holding the LSN gate as before.

`0001`–`0003` are independent and can parallelise. `0004` rides on `0003`'s write helper.

## 3. Definition of Done

### 3.1 IO_IN_PROGRESS
- `BufferDesc` carries an atomic flag bit set before the disk read and cleared in the deferred unlock.
- Concurrent miss on the same page: only one read issued (verified by counting smgr reads in a stress test); the second waiter blocks on a `BufferIO` wait event recorded via M0024.
- Regression test: 64 goroutines pin the same cold page, read-count assertion = 1.

### 3.2 Strategy ring
- New `BufferAccessStrategy` interface with two kinds: `BAS_BULKREAD` (32 buffers, used by SeqScan / bulk-build) and `BAS_NORMAL` (the existing pool-wide path).
- SeqScan chooses `BAS_BULKREAD` when the relation's page count exceeds `seq_page_cost`'s working-set heuristic (default: > 1/4 of `shared_buffers`).
- TPC-H SF1 SeqScan-heavy query immediately followed by a pgbench-style hot-page lookup: hot-page hit rate ≥ 95% (was ~5% on a 1k-buffer pool).

### 3.3 Bgwriter
- New goroutine `internal/storage/bgwriter.go`. Ticks every `bgwriter_delay` (default 200ms). Writes ≤ `bgwriter_lru_maxpages` (default 100) dirty buffers per tick.
- `pg_stat_bgwriter`-style counters surfaced (M0022).
- Foreground victim search reaches a dirty buffer < 5% of the time on the pgbench mixed workload (was 30–60%).

### 3.4 Checkpoint pacing
- `checkpoint_completion_target` GUC (range 0.0–1.0, default 0.9).
- Checkpointer's write phase paces flushes over `target × checkpoint_timeout`.
- Regression test: 200k dirty buffers checkpointed at `target=0.5` with `interval=30s` finishes between 14s and 17s; foreground TPS impact ≤ 20%.

### 3.5 No regression
- `make ralph-state-guard` green every loop.
- `TestRunTPCHQueriesAgainstSyntheticData` 22/22 unchanged.
- pgbench TPS ≥ post-M0042 baseline.

## 4. Out of scope

- Lock-free buffer descriptor. Atomic `BM_IO_IN_PROGRESS` is enough; full atomic descriptor flag set is a separate optimisation.
- WAL writer pacing (already covered by M0042-0003).
- Adaptive `bgwriter_lru_multiplier` / dynamic `bgwriter_delay` tuning — start with fixed defaults, revisit if needed.

## 5. Reference

- `postgres/src/backend/storage/buffer/bufmgr.c` — `BufferAlloc`, `BM_IO_IN_PROGRESS` handling.
- `postgres/src/backend/storage/buffer/freelist.c` — `BufferAccessStrategy`.
- `postgres/src/backend/postmaster/bgwriter.c` — bgwriter loop.
- `postgres/src/backend/postmaster/checkpointer.c` — `CheckpointWriteDelay`, completion-target pacing.
- `docs/reference/ref-003-buffer-pool.md`, `ref-004-checkpointer.md` — gap inventory.
- `docs/design/root-0005-buffer-manager.md`, `0002-0001-checkpointing.md`, `0032-0001-heap-arena-replacement.md` — current goopg invariants.
