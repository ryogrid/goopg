# M-NIGHTLY — TRUNCATE racing a dirty-page eviction flush (AI-20261031-002109-005)

**Landed 2026-10-31.** Nightly pgbench stage: `ERROR: flush victim: write …/base/5/16406 blk 2681: out of range (nblocks=0)` aborts a client on the `-S` workload; the pgbench `history` relation's file was caught at **0 bytes** mid-run.

## Symptom

`ci/batch/stages/stage-pgbench.sh` (s=50, c=100, 180 s × 3 workloads) failed twice in a row (2026-10-30/31 nightlies). One client aborts with

```
ERROR:  flush victim: write …/base/5/16406 blk 2681: out of range (nblocks=0)
```

`16406` is `pgbench_history`. A file watcher caught the history file at 0 bytes at the abort moment while `pgbench_accounts` was intact; the server's autovacuum log showed the history page count shrinking toward zero at each workload boundary.

## Root cause

pgbench runs `truncate pgbench_history` at the start of every workload (`postgres/src/bin/pgbench/pgbench.c:7375`, `tryExecuteStatement(con, "truncate pgbench_history")`) — legitimate. goopg's TRUNCATE (`internal/executor/operators_ddl.go` `truncateTableAndPartitions`) does, in statement order: `Pool.InvalidateRel(rel)` → `Manager.TruncateRelation(rel)` (truncate file to 0, relFile `nblocks=0`).

The eviction path raced it:

1. `claimVictim` (bufpool.go) samples a **dirty** history page from the previous workload and CASes it to `ioInflight` — which **clears valid+dirty**.
2. `InvalidateRel`'s CAS targets `valid+unpinned` → misses the claimed slot; the dirty page is not marked discarded.
3. `evictVictim` then flushes: `relFile.writeBlock(blk)` with `blk >= nblocks(0)` → `out of range (nblocks=0)` (smgr.go:1061) → surfaces as `flush victim: …` (bufpool.go pinNewXID/pinLoad, prefetch.go) and aborts the next client statement.

PostgreSQL cannot reach this: `TRUNCATE` (AccessExclusiveLock) calls `DropRelationBuffers` (`postgres/src/backend/storage/buffer/bufmgr.c:4533`), whose own comment states the motivation — invalidation must not leave buffers "that the background writer or checkpointer can lead to a PANIC error while flushing buffers corresponding to files that don't exist". It takes the buffer content lock (which waits out in-progress I/O) and **discards the dirty page without writing it** — the data is being truncated, so flushing is pointless and dangerous.

## goopg fix (port of DropRelationBuffers semantics)

PG's lever is the content lock (waits for I/O, then discards). goopg's invalidation runs with no lock that the flush honors, and introducing one inverts lock order; the flush's lifecycle is already stamped by the **slot generation** that `claimVictim` bumps. The fix signals through it:

- `Pool.InvalidateRel` / `Pool.InvalidateBlock` (bufpool.go): for a slot of the invalidated relation in `ioInflight` state, `bumpSlotGen` bumps the slot's generation (CAS). This is the "discard the dirty page" signal for the in-flight flusher.
- `Pool.evictVictim` (bufpool.go): takes the new `claimGen` from `claimVictim` and re-checks the slot generation
  1. **pre-write**, inside `contentMu` (after `OnFlushWait`, before `flushSlot`): on mismatch the flush is **skipped** and the slot retired like a clean eviction (bufmap delete, tombstone) — mirrors DropRelationBuffers discarding;
  2. **post-failure**: if `flushSlot` fails and the generation has since been bumped, the write raced a truncate that landed first — the page is discarded, the statement succeeds. A flush failure with an unchanged generation is a real defect and propagates unchanged.
  The post-failure check closes the residual window (bump after the pre-write check, truncate's `r.mu` before the write's).
- `Pool.flushBatch` (checkpoint): a batch assembled from a dirty scan now re-checks `valid+dirty` under `contentMu` and skips slots invalidated in the gap — PG's checkpoint never writes buffers `DropRelationBuffers` discarded.

With these, every interleaving of `InvalidateRel`/truncate and eviction flush ends with the stale page discarded (pre-write check), discarded (post-failure check), written before the truncate (statement order: invalidate precedes smgr truncate, so the write hits a file that still has the block), or skipped by the batch re-check.

The bgwriter path already re-checks `valid+dirty+tag` under `contentMu.RLock` before flushing and ignores failures; combined with statement ordering it cannot produce the client-visible error.

## Evidence

- Deterministic repro (full nightly sequence, 2026-10-31): history file 0 bytes at the abort; autovacuum/analyze page-count timeline shrinking 1134→853→0 pages at workload boundaries.
- Stack instrumentation on `truncateToZero` (debug build, removed): the truncates of `base/5/16406` during workloads come from `execTruncate` ← pgbench's per-run `truncate pgbench_history`.
- Mutation check: removing the pre-write gen check reproduces `flush victim: write …: out of range (nblocks=0)` in `TestTruncateDuringEvictionFlushDiscardsStalePage`; the fix passes.

## Deviations (recorded)

- **Generation signal vs content lock.** PG serializes invalidation with I/O through the buffer content lock; goopg's eviction releases `pinMu` for the flush and no pool lock spans it, so invalidation signals via the generation stamp that `claimVictim` bumps. Outcome parity (stale page never lands after the truncate's new end; client never aborts), different mechanism.
- **Post-failure discard** has no direct PG analog (PG's write cannot fail this way) — it is the closure of the window between the pre-write check and `r.mu`.

## Tests

`internal/storage/truncate_invalidate_flush_test.go`:
- `TestTruncateDuringEvictionFlushDiscardsStalePage` — drives the exact interleaving via `OnFlushWait` (invalidate + truncate parked inside the victim flush); asserts the pin succeeds, the file stays 0 bytes (page not written), and the bufmap entries are gone.
- `TestInvalidateRelBumpsGenerationOfClaimedSlot` — the signaling half.
- `TestFlushBatchSkipsInvalidatedSlots` — checkpoint batch skips invalidated slots.

## Gates

`go test ./internal/storage/ ./internal/commands/vacuum/`; `go test -race ./internal/storage/`; units (precommit scope=units); TPC-H spot-check Q12=2 Q13=35; full `stage-pgbench.sh` repro; commit-hook pgbench smoke.
