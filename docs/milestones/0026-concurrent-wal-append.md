# Milestone 0026 — Concurrent WAL Append & Flush Architecture

**Status:** in-progress
**Depends on:** Milestone 0013 (WAL buffer infrastructure), Milestone 0025 (OLTP performance analysis, which identified the WAL serialisation bottleneck).
**Drives:** Remove the single-goroutine WAL serialisation bottleneck so that write-heavy OLTP workloads (pgbench default / simple update) can approach read-only throughput.

## Context

goopg's OLTP analysis (M0025) showed that `UPDATE pgbench_accounts`
consumes **98 %** of each transaction's 75 ms latency. The WAL append
and flush path goes through a **single state-loop goroutine**:

```
Client goroutine                WAL state-loop goroutine (state.loop)
─────────────────               ──────────────────────────────────────
  Writer.Append(payload) ──────►  receive opAppend
                                   encode record
                                   write walBuf / writeAt (disk I/O)
                                   update LSN
  ◄──── (startLSN, endLSN) ────  reply via resp channel
```

Even the **append** (record encoding + buffer copy) is serialised.
With multiple clients, every transaction queues behind the single
state-loop goroutine, regardless of whether the actual bottleneck is
CPU (encoding) or I/O (fdatasync).

## Proposed Architecture

### Design Principle

Follow PostgreSQL: `XLogInsert` (append) writes to shared WAL buffers
under a lightweight lock. `XLogFlush` (fdatasync) is the only
serialised operation.

### New Flow

```
Client goroutine                WAL state-loop goroutine
─────────────────               ──────────────────────────────
  Writer.Append(payload)
    mutex.Lock(&walBuf)
      encode record
      copy to walBuf
      update LSN atomically
    mutex.Unlock()
  return LSN                   (no state-loop round-trip)

  Writer.FlushUpTo(lsn) ──────► receive opFlush
                                  drain walBuf to disk
                                  fdatasync
  ◄──── ok ───────────────────  reply via resp channel
```

Key changes:

1. **`Writer.Append` becomes direct.** It encodes the record,
   acquires a mutex on the shared `walBuffer`, copies the data in,
   updates `writeLSN`, and returns — all without talking to the
   state loop. Multiple goroutines can append concurrently (they
   block on the mutex only for the memcpy, not for the entire I/O).

2. **`state.loop` handles only drain + fsync.** The loop
   periodically (`drainReasonOverflow` or `opFlush`) drains the
   shared `walBuffer` to segment files and calls fdatasync. It no
   longer processes `opAppend`.

3. **`FlushUpTo` sends an opFlush barrier.** The caller blocks
   until the state loop confirms that `drainedLSN ≥ targetLSN`.
   This is identical to the current `opFlush` path.

4. **`MemRing` append stays in the calling goroutine.**
   The walsender in-memory ring is thread-safe (or made so with a
   separate mutex), so each client appends directly.

### Thread Safety

| Data structure | Guard | Access pattern |
|---------------|-------|----------------|
| `walBuffer` | `sync.Mutex` | `Append` (exclusive), `drainBufferBytes` (exclusive) |
| `writeLSN` | atomic | `WriteLSN()` readers; updated under walBuf mutex |
| `memRing` | `sync.Mutex` or existing atomic methods | `Append` (exclusive per-record) |

## Implementation Plan

1. **Split `state.append`.** Extract the record-encoding part
   (`encodeRecord`, `encodeRecordXLog`) into a standalone function
   callable from `Writer.Append`. Keep the disk-write/drain part
   in the state loop.

2. **Add `sync.Mutex` to `walBuffer`.** All existing callers run
   on the state-loop goroutine, so adding a mutex is a no-op for
   them; the new `Writer.Append` will lock it.

3. **Rewrite `Writer.Append`.** Encode the record, lock walBuf,
   copy data, update LSN atomically, return.

4. **Remove `opAppend` handling from `state.loop`.** The loop
   now only processes `opFlush` (and future `opDrain`).

5. **Add periodic drain.** When `walBuf.resident()` exceeds a
   threshold (or unconditionally on `opFlush`), the state loop
   drains to disk.

6. **Benchmark.** Re-run the pgbench default / simple-update
   workloads and compare TPS.

## Out of Scope

- Asynchronous commit (group commit / `commit_delay`).
- WAL compression.
- Multi-threaded WAL write (PostgreSQL 17+ WAL insertion lock
  improvements).

## Reference

- PostgreSQL WAL insert: `src/backend/access/transam/xlog.c`
  (`XLogInsertRecord`, `XLogFlush`, `WALInsertLock`)
- PostgreSQL WAL writer: `src/backend/postmaster/walwriter.c`
  (`WalWriterMain`, `walwriter_cycle`)
- goopg current architecture: `internal/wal/writer.go`
  (`Writer.Append`, `state.loop`, `state.append`)
- goopg WAL buffer: `internal/wal/wal_buffer.go`
- M0025 analysis: `analysis/oltp-performance/README.md`

## Definition of Done

1. `Writer.Append` no longer sends an `opAppend` message — it
   encodes and buffers the record in the calling goroutine.
2. `state.loop` only processes drain/flush operations.
3. `FlushUpTo` still works as a durable barrier.
4. `memRing` append is moved to the calling goroutine (or made
   thread-safe).
5. All existing WAL tests pass (`go test ./internal/wal/...`).
6. Full test suite green (`go test ./...`).
7. pgbench simple-update TPS improves by at least **5×** (from
   ~14 TPS to ≥ 70 TPS) at scale=3, 4 clients.
8. Updated performance report in `analysis/oltp-performance/`.
