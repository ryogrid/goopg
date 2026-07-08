# AIO `io_uring` Method (M0009)

- status: accepted
- date: 2026-04-29
- supersedes: —

## Goal

Add the `io_uring` value to the `io_method` GUC alongside `sync` and
`worker` (`internal/aio/aio.go::MethodIOUring`). On a Linux host where
the kernel honours `io_uring_setup(2)`, AIO submissions drive real
io_uring `IORING_OP_READ` / `IORING_OP_WRITE` opcodes — observable via
`strace -e io_uring_setup,io_uring_enter` against a `goopg start`
process. On a host where the probe fails (kernel ENOSYS, sysctl
`kernel.io_uring_disabled=1`, container seccomp EPERM) the engine
silently falls back to the worker method so server start does not fail
because of an unavailable kernel feature.

This is the third (and final required) M0009 method, matching upstream
PG 18's `pgaio_uring.c`. Subsequent loops can layer perf-oriented
extensions (registered files, IORING_SETUP_SQPOLL, multi-shot ops,
fixed buffers) on top of this seam.

## Non-goals

- **Registered files / fixed buffers.** `io_uring_register(2)` slot
  registration cuts kernel-side fd-table lookups and lets the kernel
  pin user buffers. Worth doing once we measure the cost; not a blocker.
- **SQPOLL kernel thread.** Skips `io_uring_enter` syscalls entirely
  on the submit side. Adds operational complexity (privileged process,
  thread-CPU pinning) we don't need yet.
- **Linked / chained ops.** All ops are submitted independently; the
  storage / WAL caller layer doesn't currently express dependencies
  the kernel could resolve.
- **Latency percentile histograms** for io_uring specifically — they
  reuse the engine-level `pg_stat_aio.{avg,max}_latency_us` machinery
  documented in `0009-0004-aio-observability.md`.

## File map

| File | Role |
| --- | --- |
| `internal/aio/method_iouring_linux.go` | Real io_uring method (linux build tag). |
| `internal/aio/method_iouring_other.go` | Stub returning `errProbeFailed` on every other GOOS. |
| `internal/aio/aio.go` | `NewEngine`'s `MethodIOUring` case calls `newMethodIOUring`; falls back to worker on `errProbeFailed`. `Engine.FallbackFrom()` reports the fallback. |
| `internal/aio/method_iouring_linux_test.go` | Linux-only round-trip + parallel write/read tests. Skips when the engine fell back. |
| `internal/storage/smgr.go` | `relFile.Fd()` exposes the underlying `*os.File`'s fd so the io_uring path can submit by fd. |
| `internal/initdb/open.go` | `aioFileAdapter.Fd()` + `walAIOFileAdapter.Fd()` forward through to the wrapped storage / wal AIOFile. |
| `cmd/goopg/main.go` | Removes the temporary `io_uring → worker` shim; logs `event=aio_method_fallback` post-Open when `Engine.FallbackFrom()` reports a fallback. |

## Submission model

`io_uring`'s submission queue is documented as single-producer; concurrent
SQE writes from multiple goroutines would race the tail update. We
serialise submissions with `methodIOUring.submitMu`. Submit's hot path is:

1. Build the `aio.Handle`, register it under the engine's in-flight map
   (so `pg_aios` sees the op).
2. Acquire a slot from `m.slots` (a buffered chan that throttles
   in-flight count to `min(MaxConcurrency, sqEntries)`).
3. Type-assert `op.File` to `fdHaver`. If it doesn't expose `Fd()`
   (the test in-memory file path) run the I/O inline via `runOp`.
4. Allocate a `userData` (monotonic), insert into `m.pending` keyed by
   that value.
5. Take `submitMu`, build the SQE in-place at `m.sqes[tail & sqMask]`,
   release-store the new tail, call `io_uring_enter(submit=1)`, drop
   the lock.

A single reaper goroutine drains completions:

1. `io_uring_enter(0, min_complete=1, GETEVENTS)` blocks until at least
   one CQE is ready.
2. Walk `cqHead..cqTail`, look up each `userData` in `m.pending`,
   release the slot, and `engine.finishHandle(...)`.
3. Loop, checking the `m.closed` channel between drains.

`Close` wakes the reaper out of its blocking syscall by submitting a
`IORING_OP_NOP` SQE with the sentinel user_data
`0xFFFFFFFFFFFFFFFF` — the reaper recognises that user_data and skips
the pending-map lookup. After the reaper exits, Close munmaps the three
ring regions and closes the ring fd. Closing the fd alone does NOT
reliably unblock `io_uring_enter` on every kernel; the NOP poke is the
documented idiom.

## Probe + fallback

`newMethodIOUring` runs the probe inline by calling `io_uring_setup` for
the requested SQ depth. On any errno (ENOSYS, EPERM under
`io_uring_disabled=1`, EINVAL on a too-old kernel, ENOMEM under
locked-memory limits) it returns `errProbeFailed` — a sentinel that
`NewEngine` matches via `errors.Is` and uses to fall back to the worker
method. The fallback is silent at the engine layer; the operator sees
`event=aio_method_fallback requested=io_uring actual=worker reason=...`
in the server log via `cmd/goopg/main.go`.

`Engine.FallbackFrom()` returns `(requested, reason)` so other consumers
(future `pg_stat_aio.method_requested` column? acceptance tests?) can
make the same observation without log scraping.

## Buffer ownership and Fd exposure

io_uring requires the buffer pointer + length and the file descriptor
at submission time, then transfers without further userspace
coordination. Two implications for goopg:

1. **The caller must keep the buffer alive until Wait returns** — the
   pre-existing AIO contract; documented in `0009-0001-aio-core.md`.
   The io_uring method doesn't tighten this requirement, but it makes
   violations more visible (the kernel reads the buffer asynchronously
   instead of synchronously inside `runOp`).

2. **Buffers must be Go-allocated and remain in valid memory** — Go's
   moving GC can in principle relocate stack-allocated objects, but
   slice-backing arrays on the heap are pinned by the runtime for the
   duration of the syscall. Submit holds a slice reference via the SQE
   struct's `Addr` field, so the GC keeps the backing array alive. The
   kernel's read/write completes before Wait returns, so the slice is
   still alive on the Go side throughout.

3. **Fd exposure** — the AIO public surface is `aio.File` (just
   `io.ReaderAt` / `io.WriterAt`). io_uring needs a kernel fd. We
   solved this with an unexported optional interface
   `fdHaver { Fd() uintptr }`: the io_uring method type-asserts and
   falls back to inline pread/pwrite on miss. `*os.File` satisfies it
   natively; `relFile`, `aioFileAdapter`, and `walAIOFileAdapter` add
   forwarding methods so the storage and WAL caller chains preserve fd
   visibility through their adapters. In-memory test files don't
   satisfy `fdHaver` and are handled by the inline-fallback branch.

## Checksum bypass (fixed) and the still-open per-block ordering gap

The same raw-fd submission that motivates `fdHaver` above has a second
consequence the original slice missed: submitting directly by kernel fd
means the SQE points straight at the caller's buffer, bypassing
`File.ReadAt`/`File.WriteAt` entirely. For the `sync` and `worker`
methods every op still flows through those two methods (`runOp` calls
`op.File.ReadAt`/`WriteAt`), so `storage.relFile`'s checksum stamping
(write) and verification (read) — implemented inside `WriteAt`/`ReadAt`
— apply automatically. The `io_uring` method's `fdHaver` fast path
never calls `WriteAt`/`ReadAt` at all, so on a checksummed cluster
(`data_checksum_version` ≥ 1) running `io_method=io_uring` it used to
silently: (a) write every AIO-submitted block with whatever stale
`pd_checksum` bytes happened to already be in the caller's buffer, and
(b) skip verification on every AIO-submitted read. (a) is the more
serious half — once a checkpointer flush lands a block through
`WriteBlockAIO` this way, the block's on-disk `pd_checksum` no longer
matches its content, so *any later synchronous read* (which does
verify) reports a false-positive `ChecksumError` for a page that was
never actually corrupted.

Fixed via an optional extension of `aio.File`, `ChecksumFile`
(`internal/aio/aio.go`):

```go
type ChecksumFile interface {
    PrepareWrite(buf []byte, off int64) []byte // stamp a checksum into a copy
    VerifyRead(buf []byte, off int64) error    // verify a completed read
}
```

`methodIOUring.Submit` type-asserts `op.File` against `ChecksumFile` in
addition to `fdHaver`: for a write it calls `PrepareWrite` before
building the SQE and submits the returned (possibly copied) buffer
instead of `op.Buffer`, pinning a reference in `pendingOp.buf` so it
survives until the kernel completes the write; for a read,
`completeOne` calls `VerifyRead` on the landed bytes after a successful
completion and maps a mismatch onto the `Result.Err` the caller's
`Wait()` sees. `storage.relFile` implements both methods by delegating
to the same `checksummedForWrite`/`verifyOnRead` helpers `WriteAt`/
`ReadAt` already used, so the io_uring path can't drift from the
synchronous path's checksum semantics. Files that don't implement
`ChecksumFile` (plain `*os.File`, in-memory test files) are unaffected.

This is a narrower fix than a general "route io_uring through
`WriteAt`/`ReadAt`" approach, which would force a per-file mutex around
every op (see `submitOne`'s single `submitMu`, already file-scoped for
SQE bookkeeping) and defeat the cross-block parallelism io_uring exists
for. `ChecksumFile` only touches the buffer content, not ordering.

**Still open**: `storage/aio-relfile-mu-bypass` in `.ralph/fix_plan.md`
tracks a separate, so-far-unconfirmed-as-reachable concern — the raw-fd
path also doesn't serialize against a concurrent *synchronous*
`readBlock`/`writeBlock`/`extend` (or another concurrent AIO op) on the
*same* block of the *same* relFile, since those take `relFile.mu` for
their whole duration while `Submit`'s raw-fd branch never touches it.
`.ralph/deferral_ledger.md`'s 2026-07-08 M-NIGHTLY row found this by
hand-tracing the one production call site (`Pool.flushBatch`'s
`WriteBlockAIO`) and could not construct a concrete corruption window
there — `Slot.contentMu`'s RWMutex already forces any evictor/reloader
of the same slot to wait for the flush's real `Wait()` to return before
touching `s.page` — but the gap is real at the `smgr`/`aio` layer in
isolation and deserves the per-(rel,block) in-flight-AIO registry
design described there before any other caller starts using
`WriteBlockAIO`/`PrefetchBlock` on a path `contentMu` doesn't already
cover.

## Tests

- `TestNewEngineIOUringConstructs` (cross-platform) — selecting
  `io_method=io_uring` always returns a usable engine: the actual
  method is `io_uring` on supported hosts, `worker` (with non-empty
  `FallbackFrom`) on every other build path. No `ErrUnsupportedMethod`.
- `TestEngineIOUringReadWriteRoundTrip` (linux only) — pwrite then
  pread through the engine against a real tmpfile, verify bytes
  round-trip, per-direction counters bump, in-flight gauge returns
  to zero.
- `TestEngineIOUringParallel` (linux only) — 64 concurrent writes
  followed by 64 reads against the same file, verify no slot
  collisions in the `userData → Handle` map.
- `TestEngineIOUringChecksumFileHooks` (linux only) — a fake
  `ChecksumFile`-backed file proves `Submit` calls `PrepareWrite`
  before the kernel write (and that the stamped bytes, not the
  caller's untouched buffer, land on disk) and `completeOne` calls
  `VerifyRead` after a completed read, surfacing a mismatch as the
  `Result`'s error. Confirmed to fail without the fix (reverting the
  `Submit`/`completeOne` wiring reproduces both symptoms: zero
  `PrepareWrite` calls, unstamped bytes on disk).
- `TestChecksumRelFilePrepareWriteVerifyRead` (`internal/storage`) —
  `relFile.PrepareWrite`/`VerifyRead` in isolation: stamps a real page
  via the production `PageSetChecksumCopy`/`VerifyPage` path, confirms
  the caller's buffer is untouched, and confirms a corrupted copy is
  rejected with a `*ChecksumError` carrying the right block number.

Both io_uring-specific tests `t.Skip` when `e.Method() != MethodIOUring`,
so the suite passes on any Linux host regardless of whether
`io_uring_disabled` is set or the seccomp profile allows the
syscalls.

## Upstream references

- `postgres/src/backend/storage/aio/method_io_uring.c` — upstream
  pgaio_uring implementation. This file is the reference for opcode
  selection (READ/WRITE without the V variants), the SQ tail bump
  pattern, and the close-time NOP wake idiom.
- `postgres/src/include/storage/aio.h` — public `pgaio_*` API; goopg's
  `aio.Op` / `aio.Handle` / `aio.Engine` track this surface.
- `linux/include/uapi/linux/io_uring.h` (kernel) — the kernel ABI for
  `io_uring_sqe`, `io_uring_cqe`, `io_uring_params`, ring offsets, and
  opcodes. Pinned across kernel versions; goopg's `ioSqe` / `ioCqe` /
  `ioUringParams` mirror these byte-for-byte.
