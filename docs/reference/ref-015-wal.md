# REF-015: WAL Format & I/O

## Overview

The WAL (Write-Ahead Log) subsystem provides ordered, crash-safe
persistence for all data modifications. Each modification produces
a WAL record that is written to in-memory buffers and eventually
flushed to on-disk segment files. On recovery, un-flushed records
are replayed to restore durability.

## goopg Implementation

**Package:** `internal/wal/`

### Key Types

- `Writer` — the public API for appending and flushing WAL records.
  Holds a channel (`ops`) to the state-loop goroutine and a
  `MemRing` for walsender in-memory streaming.
- `state` — the run-loop goroutine that processes operations.
  Owns `walBuf` (in-memory buffer), `writePos` / `writeLSN`,
  segment file handles, and the AIO engine seam.
- `Config` — controls WAL directory, segment size, preallocation,
  direct I/O, sender memory buffer, WAL buffers, and hook fields
  for wait events.

### Record Format (Legacy, no page headers)

```
┌──────────────────────────────┐
│ payload length (4 bytes, LE) │
├──────────────────────────────┤
│ CRC-32 of payload (4 bytes)  │
├──────────────────────────────┤
│ payload (variable)           │
└──────────────────────────────┘
```

Total header overhead: 8 bytes per record.

### Record Format (PG-compatible page headers, M0014)

When `PageHeaders=true`, records are embedded in an XLOG page
stream matching PostgreSQL's `pd_lsn` / `xlp_rem_len` layout.
The page headers are emitted every `SegmentSize` bytes. This
format is what `pg_waldump` expects.

### Data Flow (Append)

```
Writer.Append(payload)
  ├─ encodeRecord(payload) — prepend length + CRC
  ├─ (M0026 fast path) tryAppend:
  │    ├─ lock appendMu
  │    ├─ append to walBuf (memcpy) and memRing
  │    ├─ update writeLSN, unlock
  │    └─ return (startLSN, endLSN)
  └─ (fallback) send opAppend to state loop → state.append
```

### Data Flow (Flush)

```
Writer.FlushUpTo(lsn)
  └─ send opFlush to state loop → state.flushUpTo(lsn)
       ├─ drainBufferBytes (write walBuf → segment files)
       ├─ dataSync (fdatasync)
       └─ update flushedLSN
```

### Segment Management

WAL segments are 16 MB by default (`DefaultSegmentSize`), named
`000000000000000000000000` (24 hex digits: timeline + LSN). New
segments are pre-allocated and zero-filled when `Preallocate=true`.
Old segments are removed by the checkpointer's retention policy
(slot-aware).

## PostgreSQL Implementation

PostgreSQL's WAL (`xlog.c`) shares the same high-level architecture
but differs in key details:

- **WAL Insert Lock** — PostgreSQL uses a partitioned
  `WALInsertLock` (16–64 partitions) so multiple backends can
  insert WAL records concurrently. goopg uses `appendMu` (single
  mutex).
- **WAL buffer** — PostgreSQL's WAL buffers are a fixed-size ring
  in shared memory (`XLogCtl->xlblocks`). Each backend copies its
  record into the ring under the insert lock. goopg's `walBuf` is
  similar.
- **Group commit** — PostgreSQL batches multiple commits into a
  single `XLogFlush` call. Flushers register on a wait queue;
  the first to arrive performs the I/O, others sleep and are
  woken when it completes. goopg's `FlushUpTo` does not batch.
- **WAL writer** — a dedicated process (`WalWriterMain`) that
  periodically flushes WAL buffers. Backends waiting for commit
  can also flush directly. goopg's state loop handles both
  writing and flushing.
- **Checkpoint** — writes a `XLOG_CHECKPOINT_SHUTDOWN` or
  `XLOG_CHECKPOINT_ONLINE` record and fsyncs all data files.
  goopg's checkpointer uses `FlushUpTo` for WAL and
  `FlushAllPaced` for data files.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Insert concurrency | `appendMu` (single mutex) | Partitioned `WALInsertLock` (16–64 partitions) |
| Flush batching | None (per-FlushUpTo) | Group commit (XLogFlush) |
| WAL writer | Part of state loop | Dedicated WalWriterMain process |
| Record CRC | `crc32.ChecksumIEEE` with single-entry cache | CRC-32C (hardware-accelerated on modern CPUs) |
| Segment preallocation | Optional (`Preallocate`) | Always via `wal_init_zero` GUC |
| Direct I/O | Optional (`DirectIO` in Config) | Optional via `wal_direct_io` GUC |

## Potential Optimisations or Corrections

- **Partitioned WAL insert lock** would let concurrent writers
  append to different WAL buffer partitions without contending on
  `appendMu`. This is the next major WAL scalability improvement.
- **Group commit** (batching multiple `FlushUpTo` calls) would
  reduce `fdatasync` frequency under high write throughput.
- **Hardware CRC-32C** (via `crc32` instruction or SSSE3) would
  speed up WAL record encoding for large payloads.

## References

- goopg: `internal/wal/writer.go`
- goopg: `internal/wal/format.go`
- goopg: `internal/wal/wal_buffer.go`
- PG WAL insert: `postgres/src/backend/access/transam/xlog.c`
  (`XLogInsertRecord`, `XLogFlush`)
- PG WAL writer: `postgres/src/backend/postmaster/walwriter.c`
- PG WAL format: `postgres/src/include/access/xlogrecord.h`
