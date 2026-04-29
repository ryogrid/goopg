# Milestone 0009 — AIO Subsystem (Asynchronous I/O)

**Status:** planned
**Depends on:** Milestone 0001 (foundational server with the buffer manager
and WAL writer in place), Milestone 0002 (production-grade checkpointing
and concurrent B-tree — the dirty-page flushing path is what AIO most
directly accelerates), Milestone 0007 (WAL segment preallocation and
`fdatasync`-based commit path — AIO submission for WAL writes builds on
the predictable, preallocated segment layout introduced there).
**Drives:** Lower I/O-bound query latency on storage-heavy workloads
(TPC-H, large sequential and bitmap heap scans), smoother checkpoint
throughput, and a substrate that later milestones (parallel query, vacuum
throughput, replication catch-up) can layer on without each reinventing
their own background-I/O machinery.

## Context

Today every disk read and write in goopg is synchronous from the caller's
point of view. The buffer manager calls `ReadAt` / `WriteAt` on the
relation file inline, the WAL writer issues `pwrite` + `fsync` /
`fdatasync` on the commit path, and the checkpointer walks dirty buffers
and flushes them one by one. This is correct, simple, and matches
PostgreSQL's historical behaviour, but it leaves three concrete kinds of
latency on the floor:

- **Sequential and bitmap heap scans** stall a worker on every page miss
  even when the access pattern is perfectly predictable, because there is
  no machinery to issue a read-ahead I/O while the previous page is being
  consumed.
- **Checkpoints** serialise dirty-page writeback against a single
  goroutine's syscall stream, which both bounds throughput on fast
  storage and amplifies the latency tail for any query that contends for
  the same buffer-manager locks during a checkpoint pass.
- **WAL flushes** at commit time block the foreground worker on a
  syscall that, on Linux, has had a non-blocking submission path
  available for years.

PostgreSQL upstream introduced an AIO subsystem (`src/backend/storage/aio/`,
landed in PG 18) that abstracts a uniform "submit / wait / complete"
contract over multiple I/O methods (`worker`, `io_uring`, plus a
synchronous fallback) and lets the buffer manager, the read-stream
machinery, the checkpointer, and the WAL writer all participate in the
same pool. This milestone introduces the goopg equivalent: a single AIO
subsystem with a uniform handle / completion API, an initial set of
callers (read stream for sequential scans, checkpoint dirty-page
writeback, WAL writeback), and the configuration and observability needed
to operate it in production.

The milestone is upstream-shape-faithful: GUC names, method names, and
the shape of the public submit / wait API mirror upstream closely enough
that a reader familiar with PostgreSQL 18's AIO documentation can find
their bearings in goopg's source quickly.

## In Scope

### AIO Core

- A `pgaio`-equivalent package under `internal/aio/` exposing a
  `Handle`-style submission API: callers acquire a handle, describe an
  I/O (target file / offset / length / direction / buffer), submit it,
  and later wait for completion. Completion delivers the byte count and
  any error to a caller-supplied callback, mirroring upstream's
  completion-callback chain.
- A pluggable I/O method abstraction with at minimum two implementations
  available at start time:
  - `worker` — a pool of goroutines that perform synchronous syscalls
    on the caller's behalf. Always available; the safe default on
    every supported platform.
  - `io_uring` — Linux-only, gated on a runtime probe of
    `io_uring_setup(2)` availability and the kernel's support for the
    submission features goopg uses. Falls back to `worker` if the
    probe fails.
- A synchronous-fallback path so any caller can issue an I/O with the
  AIO API even when the configured method is unavailable for that
  specific I/O (for example, an `io_uring` that does not support a
  particular fd type).
- Bounded backpressure: a configurable maximum number of in-flight
  I/Os per backend and globally, with submission blocking once the
  limit is hit rather than allocating unbounded queue depth.
- Cancellation and shutdown semantics: in-flight I/Os are drained on
  clean shutdown, and there is a documented contract for what happens
  to a handle whose owning operation is being cancelled.

### Read-Stream Integration

- A read-stream API on top of the AIO core, modelled on upstream's
  `read_stream.h`: a caller hands the stream a "next block" callback
  and a desired lookahead depth, and the stream issues prefetch I/Os
  ahead of the consumer's `Next()` calls.
- Sequential heap scans and bitmap heap scans use the read stream as
  their page-fetch path. Single-page lookups (index probes) continue
  to use the synchronous buffer-manager path; the milestone does not
  attempt to convert every read site.
- The lookahead depth is bounded by both a per-stream limit and the
  global in-flight-I/O cap, so a misbehaving query plan cannot
  monopolise the AIO pool.

### Checkpointer Integration

- The checkpointer's dirty-page flushing loop submits writes through
  the AIO core rather than issuing inline `WriteAt` syscalls. Within
  a single `checkpoint_completion_target` window the checkpointer
  keeps a configurable number of writes in flight at any time.
- The smoothed-checkpoint pacing from M0002 continues to govern *when*
  writes are submitted; AIO governs *how* they are issued. Pacing
  semantics must not change observably for operators who do not opt
  into AIO.
- `fsync` of the underlying relation files at the end of a checkpoint
  pass continues to be a synchronous, ordering-significant step. AIO
  is for the writeback bandwidth, not for moving the durability
  barrier.

### WAL Writer Integration

- The WAL writer's per-segment writeback path can submit writes
  through the AIO core, with the commit-path durability barrier
  (`fdatasync` on Linux per M0007) remaining a synchronous,
  serialising syscall.
- The interaction with M0007's preallocation is explicit: a segment
  that has been preallocated and `fsync`-ed once is a valid AIO
  write target; a segment that has not yet been preallocated is
  not. The writer must not race AIO submission against
  preallocation.

### Configuration

- `io_method` GUC, accepting at least `worker`, `io_uring`, and
  `sync` (the upstream-named explicit-synchronous method that
  bypasses the AIO core entirely). Upstream-faithful name and
  values.
- `io_workers` GUC controlling the size of the `worker` method's
  goroutine pool.
- `io_max_concurrency` GUC controlling the per-backend in-flight
  cap, and `io_combine_limit` controlling the maximum size of
  combined adjacent I/Os when the read stream merges them.
- All AIO GUCs default to behaviour equivalent to today's
  synchronous code path (`io_method = worker` with a small pool, or
  `io_method = sync`, whichever the design doc concludes is the
  safer default), so opting in is explicit until the system has
  been exercised in production.

### Observability

- A `pg_aios` (or goopg-equivalent) system view exposing in-flight
  I/Os: owning backend, target relation / file, offset, length,
  direction, state (`SUBMITTED`, `IN_PROGRESS`, `COMPLETED`,
  `ERROR`), and elapsed time.
- Counters covering submitted / completed / errored / cancelled I/Os,
  bytes read / written through AIO, average and tail completion
  latency, and pool saturation events. Surfaced through the existing
  stats infrastructure used by `pg_stat_*` views.
- A startup log line indicating the chosen `io_method`, the result
  of any probe (for `io_uring`), the worker-pool size, and the
  in-flight caps in effect.
- Wait events for "waiting on AIO completion" so a stalled query
  shows up identifiably in the existing wait-event surface from
  prior milestones.

## Out of Scope

- O_DIRECT / direct-IO. The AIO core operates on the same page-cache
  semantics as today's synchronous path; bypassing the page cache is
  a separate, much larger change.
- AIO for index reads / writes beyond what the read-stream
  integration delivers transitively. Index-scan acceleration is a
  follow-up milestone.
- AIO for vacuum's heap and index passes. The hooks must not
  preclude it, but vacuum continues to use synchronous I/O.
- Asynchronous `fsync` / `fdatasync`. Durability barriers remain
  synchronous and serialising.
- AIO for replication WAL receiving / sending paths. M0005 and
  M0008 continue to use synchronous I/O on the replication path;
  AIO integration there is a follow-up.
- Windows-native AIO (IOCP). `worker` remains the only supported
  method on Windows.
- Adaptive auto-tuning of `io_workers` / `io_max_concurrency` based
  on observed contention. Manual GUC tuning only.
- A user-visible "force this query to use AIO / not use AIO" hint
  surface.

## Required Design Docs

Place under `docs/design/` with sequential numbering at creation time:

- `0009-0001-aio-core.md` — handle / completion API, the I/O method
  abstraction, the `worker` and `io_uring` implementations,
  backpressure, cancellation and shutdown, and the synchronous
  fallback path. Cross-references upstream `src/backend/storage/aio/`.
- `0009-0002-read-stream.md` — read-stream API on top of the core,
  lookahead policy, integration with sequential and bitmap heap
  scans, and the per-stream / global in-flight bounds.
- `0009-0003-aio-checkpointer-and-wal.md` — checkpointer dirty-page
  writeback through AIO, interaction with the smoothed-checkpoint
  pacing from M0002, and WAL-writer writeback through AIO with the
  durability barrier (`fdatasync` per M0007) preserved.
- `0009-0004-aio-observability.md` — `pg_aios` view shape, counters,
  wait events, and the startup-time log contract.

These design docs should cross-link to
`docs/design/root-0005-buffer-manager.md`,
`docs/design/root-0008-wal-and-recovery.md`,
`docs/design/0002-0001-checkpointing.md`, and
`docs/design/0007-0002-fdatasync-commit-path.md`, and refine — rather
than supersede — each.

## Reference

Upstream sources to consult:

- `postgres/src/backend/storage/aio/` — `aio.c`, `aio_io.c`,
  `aio_callback.c`, `method_worker.c`, `method_io_uring.c`,
  `method_sync.c`. The README in this directory is the single
  highest-leverage document for the milestone.
- `postgres/src/include/storage/aio.h` and `aio_types.h` — public
  handle / completion API shape.
- `postgres/src/backend/storage/aio/read_stream.c` and
  `postgres/src/include/storage/read_stream.h` — read-stream API
  and lookahead policy.
- `postgres/src/backend/storage/buffer/bufmgr.c` — call sites that
  upstream converted to use AIO (search for `pgaio_io_*`).
- `postgres/src/backend/access/transam/xlog.c` — WAL-writer call
  sites that upstream's AIO landing touched.

## Definition of Done

1. The `internal/aio/` package exposes a documented submit / wait
   API with the `worker` and `io_uring` methods both implemented.
   On a Linux host with `io_uring` support, runtime probing selects
   it under `io_method = io_uring` and the resulting I/Os are
   visible to `strace -e io_uring_*`. On every supported platform,
   `io_method = worker` works and uses goroutine workers.
2. Sequential heap scans and bitmap heap scans on a TPC-H-shaped
   relation issue prefetch reads through the AIO core: a read-stream
   instrumentation counter advances during the scan, and on a
   storage path slow enough to surface I/O latency the wall-clock
   time of the scan improves measurably versus the synchronous
   baseline. The improvement is documented with measurements in the
   implementation PR.
3. The checkpointer's dirty-page writeback runs through the AIO
   core, with up to `io_max_concurrency` writes in flight at a
   time. End-of-checkpoint `fsync` semantics are unchanged and a
   crash-recovery test still passes.
4. The WAL writer's writeback path can run through the AIO core,
   and the commit-path `fdatasync` barrier still serialises
   correctly: a `pgbench` write-heavy run completes without data
   loss after a `SIGKILL` mid-workload, and observed commit-latency
   distribution is no worse than the M0007 baseline.
5. `io_method`, `io_workers`, `io_max_concurrency`, and
   `io_combine_limit` are honoured with upstream-compatible
   semantics. Each has a regression-style test that perturbs it
   and observes the expected behaviour.
6. `pg_aios` is queryable, returns non-empty rows during a load,
   and the AIO counters and wait events appear in the existing
   stats / wait-event surfaces.
7. With `io_method = sync`, the AIO core is bypassed and behaviour
   is observably identical to the pre-milestone synchronous path
   (verified by syscall trace and by performance parity within
   noise on a microbenchmark).
8. All required design docs (`0009-0001` … `0009-0004`) are merged
   with status `accepted`, and `root-0005-buffer-manager.md` /
   `root-0008-wal-and-recovery.md` carry forward-links to them.
