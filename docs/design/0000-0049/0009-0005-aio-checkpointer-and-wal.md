# 0009-0005 — AIO checkpointer + WAL writer (M0009)

Status: accepted (substrate slice — caller wiring deferred)

## Numbering note

The M0009 milestone definition originally numbered this doc
`0009-0003`. The renumbering trail is recorded in
`0009-0003-aio-storage-integration.md`: storage substrate took
`0009-0003`, observability `0009-0004`, and checkpointer + WAL
write integration moved here.

## Goal

Lay the write-side substrate the checkpointer's dirty-page
writeback path and the WAL writer's per-segment writeback path
can both use to flow writes through the AIO engine. Out of
scope this slice: the actual hot-path wiring of either caller
— that follows once the substrate is reviewable on its own.

## What this slice delivers

### `storage.AIOSubmitOp.Direction`

The substrate that landed in `0009-0003` modelled `AIOSubmitOp`
as a read-shaped subset of `aio.Op`:

```go
type AIOSubmitOp struct {
    File   AIOFile      // ReadAt-only
    Buffer []byte
    Offset int64
}
```

This slice extends it with a `Direction AIODirection` field
(`AIODirRead` zero value preserves the prefetch-only
semantics; `AIODirWrite` flips it to a pwrite). The
`AIODirection` enum mirrors `aio.Direction` without taking
the import — keeping `internal/storage` import-free of
`internal/aio`.

`AIOFile` now requires both `ReadAt` and `WriteAt`, matching
the underlying `*os.File` shape. The `relFile` type satisfies
both via mutex-guarded forwards to `*os.File`.

### `Manager.WriteBlockAIO`

```go
func (m *Manager) WriteBlockAIO(rel RelFileNode, blk BlockNumber, buf []byte) (AIOHandle, error)
```

Submits a write of `buf` as block `blk` of `rel` through the
attached AIO engine, falling back to a synchronous
`writeBlock` + `preCompletedHandle` when no engine is
attached. Out-of-range blocks return an already-complete
handle whose `Wait` surfaces a descriptive error — mirrors
`WriteBlock`'s "extend through Extend, not the write path"
contract.

Validation is upfront (BlockSize check + nblocks bounds);
the engine path forwards `Direction = AIODirWrite` so the
adapter dispatches to `aio.DirWrite`.

### Adapter forwarding

`internal/initdb/open.go::aioEngineAdapter.Submit` now
honours `op.Direction`:

```go
dir := aio.DirRead
if op.Direction == storage.AIODirWrite {
    dir = aio.DirWrite
}
```

`aioFileAdapter.WriteAt` now actually forwards to the storage
file (it previously panicked, since `PrefetchBlock` was
read-only).

## Checkpointer dirty-page writeback (landed)

`storage.Pool.FlushAllPaced` is now a batched WAL-flush +
parallel AIO-submit + Wait loop:

1. **Snapshot** the dirty set under `poolMu`.
2. For each batch of up to `flushBatchSize()` slots
   (configured via `Pool.SetAsyncFlushBatchSize`,
   clamped to `[0, MaxFlushBatchSize=256]`; 0/1 reduces to
   the legacy per-slot serial loop):
   - **Phase 1** — RLock all `slot.contentMu` in the batch.
     Held for the entire batch so writers can't tear the
     bytes the engine pwrites concurrently.
   - **Phase 2** — Compute `maxLSN = max(pd_lsn)` across
     the batch and run ONE `wal.FlushUpTo(maxLSN)`.
     WAL-before-data is preserved: every page in the
     batch has `pd_lsn ≤ maxLSN`, so a single durability
     barrier covers them all.
   - **Phase 3** — Submit every data write through
     `Manager.WriteBlockAIO`, collecting `AIOHandle`
     values. With no engine attached, each Submit returns
     a pre-completed handle synchronously and the loop
     runs writes serially.
   - **Phase 4** — Wait on every handle. Drain all (even
     on first error) so the engine's `InFlight` counter
     stays coherent; surface the first error.
   - **Phase 5** — Clear dirty bits under `poolMu`, only
     when the slot's tag hasn't been reassigned since
     phase 1.
3. After each batch, fire `pacer(progress)` with the
   batch's cumulative completion fraction.

Pacing semantics widen from per-slot to per-batch. The
M0002 smoothed-checkpoint contract ("pacing semantics
must not change observably for operators who do not opt
into AIO") holds at batch size 1 — the loop is
bit-equivalent to the previous per-slot serial flush.

`initdb.Open` calls `pool.SetAsyncFlushBatchSize(8)`
after attaching an AIO engine. A future GUC will expose
the value; for now the choice is wired off engine
attachment.

`flushSlot` is unchanged for the eviction-path callers
(`Pin` / `PinNew` evict-and-flush) — those keep their
per-slot serial behaviour because eviction is on the
single-slot critical path, not a batchable
sweep.

### WAL writer per-segment writeback (landed)

`internal/wal/writer.go` exposes a `Config.AIO` seam plus
narrow `wal.AIOEngine` / `AIOSubmitOp` / `AIOFile` /
`AIOHandle` / `AIODirection` interfaces (parallel to the
storage-side shapes so `internal/wal` stays free of an
`internal/aio` import).

`state.aio` mirrors `Config.AIO`. `state.writeAt`:

- **Engine attached** — wraps the segment chunk in
  `AIOSubmitOp{File: f, Buffer: chunk, Offset: segOff,
  Direction: AIODirWrite}`, calls `engine.Submit`, and
  Waits inline. WAL writes appear in `pg_aios` /
  `pg_stat_aio` alongside heap writes — reads, heap writes,
  and WAL writes all flow through one engine.
- **No engine** — falls back to direct `f.WriteAt` (legacy
  / pre-AIO behaviour).

The commit-path durability barrier is unchanged: the WAL
writer's loop is single-threaded, every `Submit` Waits
inline before returning, and by the time `flushUpTo` calls
`dataSync`/fdatasync every byte ≤ the requested LSN has
already been pwrite'd. Order-significant fdatasync remains
synchronous and serialising.

The interaction with M0007 preallocation is unchanged from
the legacy path: `openSegment` does the
preallocate-zero-fill + fsync before `state.writeAt` ever
sees the segment, so AIO submission can never race
preallocation.

`initdb.Open` builds a `walAIOEngineAdapter` (parallel to
the storage `aioEngineAdapter` — same shape, separate type
so each package keeps its own narrow interface) and threads
it through `wal.Config.AIO` when an engine is attached.

What's deferred: **perf-pipelining.** The WAL writer's
loop is single-threaded so inline Wait gives no
parallelism — Append `n+1` still waits on Append `n`'s
pwrite. Restructuring the loop to defer Wait across
multiple Appends (so commit `n+1` doesn't pay for commit
`n`'s pwrite latency) is a follow-up that requires
changing the loop's serialisation model. The observability
+ symmetry win is in place; the perf win waits on the
loop redesign.

## Verification

### Substrate (this slice)

`internal/storage/storage_test.go`:

- **TestWriteBlockAIOSyncFallback** — no engine attached;
  WriteBlockAIO runs the write inline and returns a
  pre-completed handle whose Wait yields BlockSize / nil.
  Bytes round-trip through ReadBlock — confirms the
  fallback path is identical to WriteBlock semantically.
- **TestWriteBlockAIOUsesAttachedEngine** — engine attached;
  the recording engine sees one submit with
  `Direction=AIODirWrite`; bytes round-trip through ReadBlock
  to confirm the WriteAt path actually executed.
- **TestWriteBlockAIORejectsOutOfRange** — past-nblocks write
  returns a pre-completed handle whose Wait surfaces a
  descriptive error.

The recording engine (`recordingAIOEngine` in the same test
file) was extended to dispatch on `op.Direction` — read ops
forward to `File.ReadAt`, write ops to `File.WriteAt`. The
existing read-side tests (TestPrefetchBlockUsesAttachedEngine,
TestPoolPrefetchEnabledFiresThroughEngine,
TestSeqScanFiresPrefetchesAcrossBlocks) still pass because
`AIODirRead` is the zero value.

### Caller integration (Pool.FlushAllPaced — landed)

`internal/storage/storage_test.go`:

- **TestFlushAllPacedBatchedSubmitsThroughEngine** — 3
  dirty slots + recording engine + batch=4 → 3 submits
  all `Direction=AIODirWrite`; bytes round-trip after
  Invalidate+Pin to confirm WAL-before-data wasn't
  broken (writes still landed correctly).
- **TestFlushAllPacedBatchSizeOneEquivalentToLegacy** —
  default batch=0 → serial path still flushes a dirty
  slot correctly with no engine attached. Pre-AIO
  behaviour preserved.
- **TestSetAsyncFlushBatchSizeClamps** — input
  validation: -1 → 1 (legacy serial), 10×Max →
  `MaxFlushBatchSize`.

The exec-package recording-engine fake
(`recordingExecAIOEngine`) was extended in the same slice
to dispatch on `Direction` so
`TestSeqScanFiresPrefetchesAcrossBlocks`'s
flush-then-Invalidate setup keeps round-tripping bytes.

## What this slice doesn't deliver

- **WAL writer perf-pipelining.** The writer flows
  through the engine but Waits inline (single-threaded
  loop). Real pipelining requires loop redesign — see the
  WAL writer section above.
- **Async fsync / fdatasync.** Out of scope. Durability
  barriers stay synchronous and serialising.
- **Eviction-path AIO writes.** `Pool.flushSlot` (called
  from `Pin` / `PinNew` evict-and-flush) keeps its
  per-slot serial behaviour. The eviction is on the
  single-slot critical path, not a batchable sweep, so
  there's no parallelism to harvest.
- **GUC for batch size.** `SetAsyncFlushBatchSize` is
  hard-wired to 8 in `initdb.Open` when an engine
  attaches; a future GUC (e.g.
  `checkpoint_io_concurrency`) can expose the knob.
- **Replication WAL receiving / sending paths.** Out of
  scope per the M0009 milestone definition.

## Cross-references

- AIO core: `0009-0001-aio-core.md`.
- Read-stream: `0009-0002-read-stream.md`.
- Engine lifecycle + storage substrate (read-side):
  `0009-0003-aio-storage-integration.md`.
- AIO observability: `0009-0004-aio-observability.md`.
- Upstream:
  - `postgres/src/backend/storage/buffer/bufmgr.c::FlushBuffer`
    — checkpointer's per-buffer flush call site.
  - `postgres/src/backend/access/transam/xlog.c::XLogWrite`
    — WAL writer's writeback call site.
