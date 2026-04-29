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

## Caller integrations (deferred to follow-up slice)

This slice intentionally stops at the substrate. The
checkpointer and WAL writer call sites that should layer on
`Manager.WriteBlockAIO` are listed below; their landing is
the next slice.

### Checkpointer dirty-page writeback

`storage.Pool.FlushAllPaced` walks the dirty set serially,
calling `flushSlot` (which does the WAL flush + the data
write). To pipeline writes through AIO, the caller would
shape the loop as:

1. Snapshot the dirty set.
2. For each slot, run the WAL flush serially (must precede
   data writes per WAL-before-data — the durability barrier
   is order-significant and stays synchronous).
3. Submit all data writes through `Manager.WriteBlockAIO`,
   collecting handles.
4. Wait on each handle in order; clear the dirty bit only
   when the write lands AND the slot's tag hasn't been
   reassigned in the interim.
5. Pacing semantics: `pacer(progress)` fires after each
   batch's Wait set lands rather than per-slot. M0002's
   smoothed-checkpoint contract ("pacing semantics must not
   change observably for operators who do not opt into AIO")
   holds because the engine-less path keeps the per-slot
   loop verbatim.

### WAL writer per-segment writeback

`internal/wal/writer.go::state.writeAt` performs `f.WriteAt`
on the underlying segment file for every WAL append. The
commit-path durability barrier (`fdatasync` on Linux per
M0007) remains a synchronous, serialising syscall — AIO is
strictly for the *writeback bandwidth*, not for moving the
durability barrier.

The interaction with M0007 preallocation is explicit: a
segment that has been preallocated and `fsync`-ed once is a
valid AIO write target; a segment that has not yet been
preallocated is not. The writer must not race AIO submission
against preallocation.

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

### Caller integration (next slice)

Will pin: a flush-all under load with the engine attached
shows the engine's `submitted` and `completed` counters
advancing in lock-step with dirty-slot count, the `InFlight`
counter peaks at most `min(io_max_concurrency, dirty_slots)`,
and a crash-recovery test still passes.

## What this slice doesn't deliver

- **Pool.FlushAllPaced AIO wiring.** The substrate is in
  place but no checkpointer call site uses
  `WriteBlockAIO` yet. Follow-up.
- **WAL writer integration.** Same — substrate is shared,
  the per-segment writeback path is the same `WriteAt`
  shape, but the writer's hot path doesn't yet flow
  through the engine.
- **Async fsync / fdatasync.** Out of scope. Durability
  barriers stay synchronous and serialising.
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
