# WAL Performance Analysis and Optimisation Path

## Executive Summary

This report documents the investigation into goopg's write-path
performance (`UPDATE pgbench_accounts` accounts for 98% of
transaction time at ~74ms per call) and the proposed fix: making
WAL Append bypass the serialised state-loop goroutine.

## Current Architecture

```
Client goroutine                     state.loop goroutine
─────────────────                    ──────────────────────
Writer.Append(payload)
  send(opAppend) ───────────────►    receive op
                                       encode record
                                       append to walBuf
                                       update writeLSN
  ◄──── (startLSN, endLSN) ──────    reply via resp channel
```

Both `Append` and `FlushUpTo` go through the same unbuffered channel
(`ops`) and are processed by the **single** state-loop goroutine.
When `FlushUpTo` blocks on `fdatasync`, all pending `Append` calls
queue behind it — regardless of whether the caller needs a reply
immediately.

## Bottleneck Identification

Per-statement timing from `pgbench -r` (scale=3, 1 client, default
TPC-B):

| Statement | Time (ms) | Share |
|-----------|-----------|-------|
| `UPDATE pgbench_accounts SET abalance = abalance + :delta WHERE aid = :aid` | **73.9** | **97.7 %** |
| All other statements | 1.7 | 2.3 % |
| **Total** | **75.6** | **100 %** |

The SELECT-only benchmark achieves 6 454 TPS at 4 clients, showing
that the read path is fast. The write workload achieves only 13.5 TPS —
a factor-of-500 difference — confirming a serialisation point in the
write path.

## PostgreSQL's WAL Architecture (Reference)

PostgreSQL separates WAL insert from WAL flush:

1. **`XLogInsertRecord`** — The backend writes into shared WAL
   buffers (`XLogCtl->xlblocks`) under a partitioned `WALInsertLock`.
   No I/O is performed. The CPU work (record formatting, CRC) is done
   in the backend's own process.

2. **`XLogFlush`** — The backend requests durability by waiting
   until `flushedUpto ≥ insertUpto`. The actual `fdatasync` may be
   performed by the WAL writer (`WalWriterMain`) or by the requesting
   backend in a critical section. Multiple backends waiting for the
   same flush point are grouped (group commit).

3. **`WalWriterMain`** — A dedicated process that periodically (or
   on demand) writes dirty WAL buffers to disk and calls `fdatasync`.
   This offloads the fsync from backends in the common case.

Key difference from goopg: **WAL INSERT is not serialised through a
single goroutine.** Each backend formats and buffers its own records;
only the fsync barrier is shared.

## Proposed Fix: Direct Append (M0026)

### Design

The `Writer.Append` call shall encode and buffer the record directly
in the calling goroutine, then return immediately with the assigned
LSN. Only the drain + fsync path remains in the state loop.

```
Client goroutine                     state.loop goroutine
─────────────────                    ──────────────────────
Writer.Append(payload)
  encode record
  lock(sharedWriteBuf)
    append to walBuf
    update writeLSN atomically
  unlock(sharedWriteBuf)
  return LSN                       (no round-trip)

Writer.FlushUpTo(lsn) ──────────►   lock(sharedWriteBuf)
                                      drain walBuf → segments
                                    unlock(sharedWriteBuf)
                                    fdatasync
  ◄──── ok ────────────────────     reply
```

### Shared Data

A new `sharedWriteBuf` struct holds the fields that both `Append`
and the drain path need:

```go
type sharedWriteBuf struct {
    mu        sync.Mutex
    walBuf    *walBuffer
    memRing   *MemRing
    writePos  int64
    writeLSN  uint64
    drainedLSN uint64
    prevRecPtr uint64
}
```

Both `Writer` and `state` hold a pointer to the same `sharedWriteBuf`.

### Implementation Status

A first attempt was made (2026-05-01) but reverted because the
`state` struct's drain methods (`flushUpTo`, `drainBufferBytes`,
`writeAt`, `openSegment`) read from `state.writePos`,
`state.drainedLSN`, `state.walBuf`, etc. — they were not updated to
read from the shared buffer. The change required updating
approximately 20 methods on `state` to use `state.writeBuf.*` instead
of `state.*`.

The full implementation is tracked in milestone M0026
(`docs/milestones/0026-concurrent-wal-append.md`).

### Estimated Impact

Based on the observation that the UPDATE path is 500× slower than
the SELECT path, and that the state-loop round-trip adds at least
one goroutine context switch per Append, the expected improvement
is **5–50×** for write-heavy workloads.

A more conservative estimate: if 50% of the 73.9ms is the state-loop
serialisation, fixing it would bring UPDATE latency to ~37ms and TPS
to ~27. If 80% is serialisation, TPS would reach ~67.

## Measurement Data

All data files are in `analysis/oltp-performance/`:

| File | Content |
|------|---------|
| `README.md` | Full OLTP analysis report |
| `quick-bench.sh` | Benchmark runner script |
| `perf-detail.sh` | Per-statement timing collector |
| `pgbench-all.log` | Raw pgbench output |
| `waits.csv` | Wait-event samples (empty due to rapid clearing) |
| `run-benchmarks.sh` | Extended benchmark runner |

## References

- Milestone M0025: `docs/milestones/0025-oltp-performance-analysis.md`
- Milestone M0026: `docs/milestones/0026-concurrent-wal-append.md`
- PostgreSQL WAL insert: `postgres/src/backend/access/transam/xlog.c`
  (`XLogInsertRecord`, `XLogFlush`)
- PostgreSQL WAL writer: `postgres/src/backend/postmaster/walwriter.c`
- goopg current WAL: `internal/wal/writer.go`
- goopg WAL buffer: `internal/wal/wal_buffer.go`
