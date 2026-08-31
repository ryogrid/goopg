# 0042-0001 — PostgreSQL I/O Subsystem Survey

**Status:** accepted
**Parent milestone:** M0042
**Date:** 2026-05-04
**Audience:** goopg implementers preparing the M0042 buffered-I/O
realignment. This is a reference document; a fix list lives in
`0042-0002`, `0042-0003`, `0042-0004`.

## 1. Scope

This document describes how upstream PostgreSQL (reading
`postgres/` at the version pinned in this repository, PG 18.x)
performs:

1. WAL writes and durability.
2. Heap / index page writes, reads, and victim eviction.
3. The background writer.
4. The checkpointer.
5. The WAL buffer (insertion and eviction-when-full).
6. The dedicated WAL writer process.
7. The client-backend process and its interaction with the above.

Each section cites concrete files under `./postgres/src/backend/...`
so the corresponding goopg code can be reviewed against a precise
upstream behaviour, not a folk model.

## 2. WAL writes and durability

### 2.1 Buffered writes — no `O_DIRECT` by default

Upstream's WAL segment files (under `pg_wal/`) are opened with
plain `open(O_RDWR | O_CREAT, …)` and the WAL writer issues
ordinary `write(2)` calls. The OS page cache holds the bytes
between the `write` and the durability barrier (`fsync` /
`fdatasync`). `O_DIRECT` is **not** the default for WAL on any
upstream platform; it is offered behind `wal_sync_method =
open_datasync` / `open_sync` for installations that explicitly
prefer it, but the default is buffered + `fdatasync`. Reference:
`src/backend/access/transam/xlog.c::issue_xlog_fsync` switches on
`wal_sync_method` to call one of `pg_fsync_no_writethrough`,
`pg_fdatasync`, `pg_fsync_writethrough`, `fdatasync`, or `fsync`.

### 2.2 `XLogWrite`, `XLogFlush`, `XLogBackgroundFlush`

The three entry points to the WAL durability machinery are:

- `XLogWrite` (`xlog.c`) — write all complete WAL pages from the
  shared WAL buffers up to a target LSN into the segment file.
  Does **not** issue `fsync`; it just transfers bytes from
  PostgreSQL's WAL buffers to the OS.
- `XLogFlush(LSN)` — caller waits until the OS has been told to
  flush WAL up to LSN. Implemented as: take `WALWriteLock`, call
  `XLogWrite` if necessary to advance `LogwrtResult.Write` past
  LSN, then call `issue_xlog_fsync` to advance `LogwrtResult.Flush`.
  Backends doing synchronous commit call this from `RecordTransactionCommit`.
- `XLogBackgroundFlush` — the WAL writer process's main work; it
  calls `XLogWrite` opportunistically (advancing `Write` LSN) and
  occasionally issues `fsync` (advancing `Flush` LSN), so that
  client backends rarely have to write or fsync themselves.

### 2.3 `WALInsertLock` partitioning

Insertion into the WAL buffer is parallelised across
`NUM_XLOGINSERT_LOCKS` (default 8) `WALInsertLock` slots. Each
backend that calls `XLogInsert` holds one of these locks while
copying its record into the buffer. This is **not** the same lock
as `WALWriteLock`, which serialises writes from buffer to disk.
The two-lock model lets many backends append into the buffer in
parallel while a single writer drains it. See
`src/backend/access/transam/xloginsert.c::XLogInsert` and
`src/backend/access/transam/xlog.c::WALInsertLockAcquire`.

### 2.4 The WAL ring is page-aligned, not record-aligned

WAL is laid out as 8 KiB pages (compile-time `XLOG_BLCKSZ`).
Records can span pages; pages have a small `XLogPageHeader`
prefix. The buffer is therefore allocated in page multiples
(`wal_buffers` GUC), and writes are always issued in page
multiples — partial pages stay in the buffer. This is what
makes "WAL buffer eviction" coherent (§6.1) without requiring
record-level alignment.

### 2.5 Durability barriers

A backend that requested `synchronous_commit = on` only returns
to the client after `XLogFlush(commit-record-LSN)` succeeds —
i.e. the OS has acknowledged the `fdatasync`. With
`synchronous_commit = off`, the backend skips the flush and lets
the WAL writer pick up the bytes on its next cycle (loses up to
~3·`wal_writer_delay` of recent commits on crash).

## 3. Page-data path: writes, reads, victim eviction

### 3.1 Buffered I/O on heap / index files

Heap and index files (under each tablespace) are opened with
plain `open(O_RDWR | O_CREAT, …)`; reads use `pread`, writes use
`pwrite`. There is no `O_DIRECT` option in upstream for relation
files. Durability comes from `fsync` at checkpoint time, not at
each page write. See `src/backend/storage/file/fd.c` and
`src/backend/storage/smgr/md.c`.

### 3.2 Victim selection

Upstream's buffer manager uses a clock-sweep replacement
algorithm (`StrategyGetBuffer` in
`src/backend/storage/buffer/freelist.c`). When `BufferAlloc`
needs a new buffer, it asks the strategy for a victim slot,
which iterates the buffer headers, decrementing usage counts
until it finds one with `usage_count == 0` and `refcount == 0`.

### 3.3 Writing a victim

If the chosen victim is dirty, `BufferAlloc` calls `FlushBuffer`
(`src/backend/storage/buffer/bufmgr.c`) which:

1. Calls `XLogFlush(buf_hdr->lsn)` to enforce **WAL-before-data**:
   the page's last-modifying record must hit the WAL fsync barrier
   before the page hits the data file.
2. Calls `smgrwrite(buf_hdr, buf_block)` (which is `mdwrite` →
   `FileWrite` → `pwrite`).
3. Marks the buffer clean.

The fsync of the data file itself is deferred to the
checkpointer — `mdwrite` records the file in the pendingOps
hash; `mdsync` (called by `CheckPointBuffers`) fsyncs every file
that received writes since the last checkpoint.

### 3.4 Page reads

`ReadBuffer` → `BufferAlloc` → if cache miss, allocate a buffer
slot (via the strategy), then `smgrread` (`mdread` →
`FileRead` → `pread`). All buffered. The OS page cache absorbs
read-ahead and write-behind; PostgreSQL only manages the
shared-buffer pool above it.

## 4. Background writer (`bgwriter`)

A standalone background process whose only job is to write
dirty buffers ahead of the clock sweep so client backends don't
stall on victim flushes. `src/backend/postmaster/bgwriter.c`:

- Wakes every `bgwriter_delay` (default 200ms).
- Picks `bgwriter_lru_maxpages` candidates by walking the buffer
  pool's LRU/clock-sweep ahead of the eviction pointer.
- Calls `FlushBuffer` on dirty pages it finds.
- Does **not** issue `fsync` on the data files. Page durability
  is the checkpointer's responsibility.
- Does **not** issue WAL `fsync`. WAL durability is the WAL
  writer's responsibility.

The point: keep the buffer pool's eviction pipeline lukewarm
without putting either the WAL fsync or the relation fsync in
the dirty-write hot path.

## 5. Checkpointer (`checkpointer`)

`src/backend/postmaster/checkpointer.c` plus
`src/backend/access/transam/xlog.c::CreateCheckPoint`:

- Triggered every `checkpoint_timeout` (default 5min) **or**
  when WAL volume since the last checkpoint exceeds
  `max_wal_size`.
- Acquires `CheckpointLock`.
- Phase 1 — flush all currently-dirty buffers
  (`BufferSync` → `FlushBuffer` for each). Pacing throttles the
  loop over `checkpoint_completion_target` of the interval to
  smooth I/O.
- Phase 2 — fsync every file that received writes since the
  last checkpoint (`ProcessSyncRequests`, the deferred
  `mdsync` step).
- Phase 3 — write a checkpoint WAL record (`XLogInsert`,
  `XLogFlush`).
- Phase 4 — remove WAL segments older than the new
  `redo` LSN's segment, subject to `wal_keep_size` and
  replication-slot retention.

The checkpointer is the **only** process that fsyncs relation
files in steady state. The background writer never fsyncs; the
WAL writer never fsyncs relation files; client backends never
fsync relation files (they only fsync WAL on synchronous
commit).

## 6. WAL buffers

### 6.1 Layout

`wal_buffers` (default `-1` → auto = 1/32 of shared_buffers,
clamped to 1 segment) is a ring of `XLOG_BLCKSZ` (8 KiB) pages.
Backends `memcpy` their record bytes into this ring while
holding a `WALInsertLock` slot. The ring's logical position is
tracked in `XLogCtl->Insert`.

### 6.2 Eviction-when-full

When a backend wants to insert a record but the page it lands on
is still dirty (not yet `XLogWrite`-d to disk), it must wait for
the WAL writer to advance `LogwrtResult.Write` past that page —
or do the write itself. The writer-or-self choice is governed by
`AdvanceXLInsertBuffer`. This is the analogue of buffer-pool
eviction; the WAL ring is overwritten in-place once the bytes
are durable, so progress on `XLogWrite` is required to free
slots.

### 6.3 Lock layering

- `WALInsertLock[i]` (one of N = 8) — held while a backend
  copies a record into the buffer.
- `WALBufMappingLock` — held briefly while a backend maps a new
  buffer page (advance the head).
- `WALWriteLock` — held while writing buffer bytes to the
  segment file (`XLogWrite`).
- The order is: backends hold their `WALInsertLock` slot while
  inserting; release it; later, possibly a different backend
  (or the WAL writer) takes `WALWriteLock` to drain.

## 7. WAL writer (`walwriter`)

`src/backend/postmaster/walwriter.c`:

- Wakes every `wal_writer_delay` (default 200ms).
- On each wakeup, calls `XLogBackgroundFlush`, which advances
  `LogwrtResult.Write` opportunistically (writes any complete
  buffer page that hasn't been written) and conditionally
  advances `LogwrtResult.Flush` (issues `fdatasync` if the
  flush LSN lags by more than `wal_writer_flush_after`,
  default 1 MiB).
- Reduces the number of `XLogFlush` calls client backends issue
  from their commit hot path: most of the time, by the time a
  backend reaches `RecordTransactionCommit`, the WAL writer has
  already pushed the relevant LSN past `Flush`, and the
  backend's `XLogFlush` is a no-op fast path.

The WAL writer is **not** the same as the background writer:
- `bgwriter` flushes dirty heap/index pages to the OS.
- `walwriter` flushes WAL pages to the OS and `fdatasync`s WAL
  segments.

Both exist precisely so that the client backend's
synchronous-commit path is the cheap path.

## 8. Client backend

`src/backend/postmaster/postmaster.c::ServerLoop` accepts a
client connection and forks a child process running `BackendMain`
→ `PostgresMain`. The per-backend process owns:

- One transaction at a time.
- Its own snapshot (`GetActiveSnapshot`).
- Its own `MyProc` slot in the procarray.
- Its own pinned-buffer set.
- The WAL `XLogInsert` for any record it generates.
- The WAL `XLogFlush` at synchronous-commit time.
- **Not** the heap fsync.
- **Not** the buffer-pool clock sweep beyond what its own
  pinning forces.

Cross-process coordination is via shared memory + LWLocks;
there is no "the listener serialises everything" stage. The
listener forks and gets out of the way.

For the goopg counterpart: a per-connection goroutine plays
the same role — owns a transaction, a snapshot, its pinned
buffers, its WAL inserts, and its synchronous commit. The
background writer and checkpointer goroutines are independent.
The WAL writer goroutine is independent. None of those are
piggy-backed on a client goroutine; in upstream, none are
inlined into a client process.

## 9. Mapping back to goopg

This survey establishes the upstream behaviour. Specific goopg
deltas — and what M0042 changes — are listed in:

- `0042-0002-buffered-io-migration.md` (drop `O_DIRECT`).
- `0042-0003-wal-buffer-and-writer-alignment.md` (background
  WAL writer goroutine).
- `0042-0004-client-backend-goroutine-alignment.md` (per-
  connection goroutine responsibilities).

Where the upstream behaviour is faithfully reproducible in Go,
goopg should mirror it. Where Go's runtime makes a different
shape natural (e.g. one goroutine per connection vs one
process per backend; channels vs LWLocks), the milestones above
spell out the analogues.

## 10. References (upstream)

- `postgres/src/backend/access/transam/xlog.c` —
  `XLogWrite`, `XLogFlush`, `XLogBackgroundFlush`,
  `issue_xlog_fsync`, `AdvanceXLInsertBuffer`,
  `WALInsertLockAcquire`.
- `postgres/src/backend/access/transam/xloginsert.c` —
  `XLogInsert`.
- `postgres/src/backend/storage/buffer/bufmgr.c` —
  `BufferAlloc`, `BufferSync`, `FlushBuffer`, `ReadBuffer`.
- `postgres/src/backend/storage/buffer/freelist.c` —
  `StrategyGetBuffer` (clock sweep).
- `postgres/src/backend/storage/smgr/md.c` — `mdread`,
  `mdwrite`, `mdsync`.
- `postgres/src/backend/postmaster/bgwriter.c`,
  `walwriter.c`, `checkpointer.c`.
- `postgres/src/include/access/xlog.h`,
  `access/xloginsert.h`, `storage/bufmgr.h`,
  `storage/buf_internals.h`.
