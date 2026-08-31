# 0009-0002 — Read-Stream API (M0009)

Status: accepted (first slice)

## Goal

Layer a predictive-prefetch surface on top of the AIO core
(`0009-0001-aio-core.md`). A caller hands the stream a
"next block" callback, a per-block byte size, and a desired
lookahead depth; the stream issues prefetch reads via
`Engine.Submit` ahead of the consumer's `Next()` calls so that
by the time the consumer is ready for block N, block
N+lookahead has already been queued for I/O. Mirrors the shape
of upstream `read_stream.h` without taking a dependency on the
buffer manager — v0's stream operates on a `File` + byte
offsets so the same mechanism can later back both a heap-scan
prefetch path and ad-hoc prefetch consumers (e.g. ANALYZE
sample reads).

## Public surface

```go
type NextBlockFunc func() int64        // returns offset, or EndOfStream
const EndOfStream int64 = -1

type ReadStreamConfig struct {
    Engine    *Engine
    File      File
    BlockSize int               // bytes per Next() result
    NextBlock NextBlockFunc
    Lookahead int               // [1, MaxReadStreamLookahead]; 0 → DefaultReadStreamLookahead
}

type ReadStream struct { ... }
func NewReadStream(ReadStreamConfig) (*ReadStream, error)
func (s *ReadStream) Next() ([]byte, error)
func (s *ReadStream) Close() error

const (
    DefaultReadStreamLookahead = 4
    MaxReadStreamLookahead     = 256
)
```

`Next` blocks until the head prefetch lands and returns its
bytes (truncated to the underlying `ReadAt`'s reported byte
count). The returned slice aliases the stream's internal
buffer for that block and is valid until the next
`Next` / `Close` call. `io.EOF` is the trailing sentinel
returned exactly once when `NextBlock` has signalled
`EndOfStream` AND the in-flight queue has drained.

## Lookahead policy

`NewReadStream` primes the window by calling `NextBlock` /
`Engine.Submit` up to `Lookahead` times. Every `Next`
consumes the head and refills until the callback returns
`EndOfStream`. This is the simplest policy that gives bounded
look-ahead and is independent of the engine's global cap
(`io_max_concurrency`) — submission still blocks naturally
when the global cap is hit, so the stream's window can
shrink under contention.

What's deferred:

- **Per-block contiguous merge** ("io_combine_limit"):
  upstream coalesces adjacent offsets into one bigger I/O.
  v0 always issues one I/O per block. Hook reserved for
  follow-up.
- **Sequential ramp-up**: upstream starts with a 1-block
  window and ramps up if the access pattern looks sequential.
  v0 uses the configured lookahead from the first `Next`.
- **`Reset()` for restartable scans**: callers that need a
  fresh stream construct a new one.

## Why a `File`-based, not buffer-manager-aware, stream

Upstream's `read_stream_begin_relation` returns `Buffer`
handles via `read_stream_next_buffer`, hooking directly into
the buffer manager. That coupling is right for the heap-scan
caller but wrong for ad-hoc prefetchers (ANALYZE sample
reads, vacuum's free-space-map walk).

This slice keeps the abstraction at the `File` + offset layer,
matching the AIO core's own seam. The buffer-manager-aware
heap-scan integration in `0009-0003-aio-checkpointer-and-wal.md`
will sit on top of this — the read stream produces bytes;
the heap-scan caller turns those bytes into pinned `Buffer`
handles via the existing pin/unpin machinery.

## Backpressure

Two layers stack:

1. **Per-stream window**: `ReadStream` keeps at most
   `Lookahead` reads in flight.
2. **Engine-wide cap**: `Engine.Submit` blocks when the
   method's submission channel is full
   (`MethodWorker`'s default capacity is `4 × workers`,
   tunable via `io_max_concurrency`).

A pathologically large per-stream lookahead can't allocate
unbounded buffer memory because `MaxReadStreamLookahead`
clamps the window. A pathologically aggressive caller
issuing many streams in parallel still respects the engine
cap.

## Close semantics

`Close` waits for in-flight prefetches to land rather than
abandoning their buffers. Two reasons:

1. The AIO core's `InFlight` counter would leak if the
   stream walked away from un-Waited Handles.
2. The engine doesn't yet support cancellation. When
   cancellation lands (post-`io_uring`), `Close` will
   prefer-cancel-then-wait.

The drained reads' bytes are dropped on the floor — the
stream is being closed, the consumer doesn't want them.

## Verification

`internal/aio/read_stream_test.go` ships seven tests:

- **TestReadStreamSequentialRoundTrip** — four blocks at
  4-byte offsets, Lookahead=2, asserts byte-for-byte
  callback-order delivery + trailing `io.EOF` + Submitted=4.
- **TestReadStreamLookaheadCapsConcurrentSubmits** — uses a
  `gateFile` that blocks every `ReadAt` until released to
  observe the engine's `InFlight` counter mid-stream;
  asserts it never exceeds the configured Lookahead.
- **TestReadStreamHonoursDefaultLookahead** — zero
  Lookahead falls back to `DefaultReadStreamLookahead`.
- **TestReadStreamClampsHugeLookahead** — `10×Max` clamps
  to `MaxReadStreamLookahead`.
- **TestReadStreamRejectsInvalidConfig** — nil
  Engine/File/NextBlock and zero BlockSize all surface
  clear errors.
- **TestReadStreamSurfacesPerBlockError** — empty file +
  non-zero offset surfaces `io.EOF` on the per-block
  result, mirroring `io.ReaderAt`'s contract.
- **TestReadStreamCloseDrainsInFlight** — after Close, the
  engine's `InFlight` counter settles to 0 (the prefetch
  window's outstanding Submits all landed).

## Cross-references

- AIO core: `0009-0001-aio-core.md`.
- Future heap-scan / bitmap-heap-scan caller integration:
  `0009-0003-aio-checkpointer-and-wal.md`.
- Upstream:
  - `postgres/src/include/storage/read_stream.h` — public
    API shape this slice mirrors.
  - `postgres/src/backend/storage/aio/read_stream.c` —
    upstream's lookahead / ramp-up / contiguous-merge
    machinery (the hooks reserved here come from that
    code).
