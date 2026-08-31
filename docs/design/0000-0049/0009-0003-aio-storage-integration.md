# 0009-0003 — AIO engine lifecycle + storage integration substrate (M0009)

Status: accepted

## Numbering note

The M0009 milestone definition reserves `0009-0003` for
"AIO checkpointer + WAL". This slice introduces an intermediate
substrate that both that doc *and* the heap-scan / read-stream
caller path depend on, so it claims the `0009-0003` slot. The
checkpointer + WAL and observability docs become `0009-0004`
and `0009-0005`. The renumbering is recorded in
`docs/design/README.md` and the milestone's "Required Design
Docs" list will be updated when those follow-up slices land.

## Goal

Make the AIO engine an actual lifecycle component of the
running server, and give the storage Manager a narrow
prefetch-shaped seam so future heap-scan / ANALYZE /
checkpointer / WAL callers can issue reads through it without
each rewiring the engine themselves.

## Substrate

### `storage.AIOEngine` interface

`internal/storage/smgr.go` defines a narrow interface so the
storage package doesn't need to import `internal/aio` (which
already pulls in `*os.File` semantics that bufpool would
otherwise alias):

```go
type AIOEngine interface {
    Submit(op AIOSubmitOp) AIOHandle
}

type AIOSubmitOp struct {
    File   AIOFile        // ReadAt-only — prefetch never writes
    Buffer []byte
    Offset int64
}

type AIOFile  interface { ReadAt(p []byte, off int64) (int, error) }
type AIOHandle interface { Wait() (n int, err error) }
```

### `Manager.SetAIO` + `PrefetchBlock`

```go
func (m *Manager) SetAIO(eng AIOEngine)
func (m *Manager) PrefetchBlock(rel RelFileNode, blk BlockNumber, buf []byte) (AIOHandle, error)
```

`SetAIO` attaches an engine. `nil` clears any previously-set
engine — useful for tests that swap the engine mid-test.

`PrefetchBlock` submits a read of block `blk` into `buf` and
returns an `AIOHandle` the caller `Wait`s on. Two paths:

- **Engine attached** — `Manager.aio.Submit(...)` runs the
  read via the engine's `Method`. Off the calling goroutine
  for `MethodWorker`; on it for `MethodSync` (matches the
  caller's expectations either way).
- **No engine** — synchronous fallback runs `relFile.readBlock`
  inline and hands back a `preCompletedHandle{n, err}` whose
  `Wait` is already complete. Keeps the caller's
  Submit-then-Wait code identical regardless of whether the
  engine is set, so swapping methods at server start is a
  GUC-flip rather than a code-path fork.

Out-of-range blocks return an already-complete handle whose
`Wait` surfaces `ErrShortRead` (matches `ReadBlock`'s
behaviour). `buf` size is validated up front against
`BlockSize`.

`PrefetchBlock` does NOT track buf-aliasing across the lifetime
of the handle: the caller MUST keep `buf` alive until `Wait`
returns. Mirrors the `aio.Op` contract.

### `relFile.ReadAt`

The internal `relFile` type now satisfies `storage.AIOFile`.
The engine reads via `relFile.ReadAt`, not the existing
`readBlock` (which does range/length validation already done by
`PrefetchBlock`). The mutex is shared between the two paths so
parallel `Pin` and `PrefetchBlock` against the same relfile
don't race on the underlying `*os.File` cursor.

### Engine lifecycle (`initdb.Open` wiring)

`OpenOptions` grew three opt-in fields:

```go
AIOMethod         string  // "" | "sync" | "worker" (io_uring deferred)
AIOWorkers        int     // for "worker"; 0 → defaultWorkerCount=3
AIOMaxConcurrency int     // 0 → method default (4×workers for "worker")
```

When `AIOMethod` is non-empty, `Open` calls `aio.NewEngine`
with the supplied config and:

1. Surfaces the engine on `Runtime.AIO *aio.Engine`.
2. Calls `mgr.SetAIO(adapter)` so `PrefetchBlock` flows
   through it.
3. Closes the engine in `Runtime.Close()` AFTER closing the
   storage manager (so any in-flight prefetches drain
   cleanly).

The adapter (`aioEngineAdapter` / `aioFileAdapter` /
`aioHandleAdapter`) lives in `internal/initdb/open.go` so
`internal/storage` stays free of an `internal/aio` import. The
adapter's `WriteAt` panics — `PrefetchBlock` is read-only, so
any `DirWrite` op flowing through it would be a contract
violation, not graceful fallback.

### `cmd/goopg start` GUC plumbing

`stringGUC` and `intGUC` helpers added alongside the existing
`boolGUC`. `start` reads `io_method`, `io_workers`, and
`io_max_concurrency` from the registry and feeds them into
`OpenOptions`. `io_uring` is silently downgraded to `worker`
with a warn-level log line tagged `event=aio_method_fallback`
until the io_uring method's runtime probe lands.

## Verification

- **`internal/storage/storage_test.go`**:
  - `TestPrefetchBlockSyncFallback` — no engine attached;
    PrefetchBlock returns a pre-completed handle whose Wait
    yields the just-read bytes synchronously. Substrate's
    "preserve existing behaviour" guard.
  - `TestPrefetchBlockUsesAttachedEngine` — once `SetAIO` is
    called, every PrefetchBlock submits through the engine.
    A fake `recordingAIOEngine` captures every Submit so the
    test asserts the offset and round-trips bytes.
  - `TestPrefetchBlockOutOfRange` — past-EOF prefetch
    surfaces `ErrShortRead` via Wait, matching `ReadBlock`.

- **`internal/initdb/open_test.go`**:
  - `TestOpenAttachesAIOEngineWhenMethodSet` — `AIOMethod="sync"`
    populates `Runtime.AIO`; `Method()` reports the right
    name; Close tears it down.
  - `TestOpenLeavesAIONilWithoutMethod` — empty AIOMethod
    leaves `Runtime.AIO` nil so the existing synchronous
    storage paths are unchanged.

## Heap-scan caller integration

Layered on top of the storage substrate above:

### `Pool.Prefetch(tag)` + `SetPrefetchEnabled(bool)`

```go
func (p *Pool) SetPrefetchEnabled(on bool)
func (p *Pool) Prefetch(tag BufferTag)
```

`SetPrefetchEnabled` toggles `Prefetch`'s behaviour. Default
off, so synchronous deployments don't pay for an inline pread
we'd then repeat on the subsequent `Pin`. `initdb.Open` flips
it on after attaching an AIO engine to the storage Manager.

`Prefetch` checks the `byTag` map under `poolMu`. On a hit,
it's a no-op (the page is already cached, no syscall needed).
On a miss, it calls `Manager.PrefetchBlock` with a throwaway
buffer; the returned AIO handle is dropped on the floor — the
engine's worker goroutine completes the read in the
background, the buffer warms the kernel page cache via the
underlying `pread` syscall, and the buffer is then GC'd.

Errors are intentionally swallowed: a failed prefetch must
not impact correctness. The subsequent `Pin` will surface any
real I/O error.

### `seqScanOp.refillPrefetchWindow`

`executor.seqScanOp` keeps a `prefetchedThru` cursor and calls
`refillPrefetchWindow(rel)` on `Open` and after every block
advance:

```go
const seqScanLookahead storage.BlockNumber = 4

func (o *seqScanOp) refillPrefetchWindow(rel storage.RelFileNode) {
    target := o.curBlock + seqScanLookahead
    if target > o.nBlocks { target = o.nBlocks }
    for o.prefetchedThru < target {
        o.ctx.Pool.Prefetch(BufferTag{Rel: rel, Block: o.prefetchedThru})
        o.prefetchedThru++
    }
}
```

The window targets `curBlock + seqScanLookahead`, capped at
`nBlocks` so the refill loop never overshoots. With
`Pool.Prefetch` disabled the loop is essentially free (atomic
load + early return) — existing tests that don't model AIO
are unaffected.

`seqScanLookahead` is a fixed `4` for now. A future loop turns
it into a tunable GUC (mirroring upstream's
`effective_io_concurrency`).

## What this slice doesn't deliver

- **Bitmap-heap-scan integration.** Bitmap scans walk a
  pre-collected block list (from a bitmap-OR of index
  scans); same `Pool.Prefetch` shape applies, but the
  caller hasn't been wired up yet.
- **ANALYZE-sample integration.** Reservoir sampling in the
  ANALYZE path reads N random pages; an `aio.ReadStream`-
  driven version would prefetch them. Hooks reserved.
- **Direct `aio.ReadStream` integration.** seqScan still
  goes through `Pool.Pin` for the actual page read; the
  read-stream's `Next() → []byte` shape would let scans
  bypass the buffer pool for one-shot reads. Out of scope
  this slice; the cache-warming Prefetch path delivers the
  measurable win without changing pin/unpin semantics.
- **Checkpointer / WAL writer write paths** through AIO. Out
  of scope for this read-side substrate; lands in the
  follow-up `0009-0004-aio-checkpointer-and-wal.md`.
- **`pg_aios` view + counters in stats / wait-event surfaces**
  — the next-numbered slice (`0009-0005-aio-observability.md`).

## Cross-references

- AIO core: `0009-0001-aio-core.md`.
- Read-stream: `0009-0002-read-stream.md`.
- Future caller integrations:
  - `0009-0004-aio-checkpointer-and-wal.md` (planned).
  - `0009-0005-aio-observability.md` (planned).
- Upstream:
  - `postgres/src/backend/storage/smgr/smgr.c` —
    `smgrprefetch` is upstream's equivalent of
    PrefetchBlock; same submit-and-forget shape.
  - `postgres/src/backend/storage/buffer/bufmgr.c::PrefetchBuffer`
    — the buffer-pool-aware caller this substrate enables.
