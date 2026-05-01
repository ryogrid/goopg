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

## PostgreSQL Implementation (Deep Dive)

### WALInsertLock Partitioning

PostgreSQL partitions the WAL insert lock into 16–64 slots
(depending on `NUM_WAL_INSERT_LOCK_PARTITIONS`). Each backend
acquires one slot before copying its WAL record into the shared
buffer ring. The slot is released after the copy completes.

This means up to 16–64 backends can insert WAL records
**simultaneously**, as long as they acquire different partitions.
Contention only occurs when all partitions are busy — each
backend spins for at most 1 µs before trying the next partition.

goopg uses a single `appendMu` for all WAL inserts. Every
backend contends on this one mutex.

### Group Commit

When multiple backends need to flush WAL (e.g., at COMMIT),
PostgreSQL uses a **group commit** protocol:

1. Each backend registers its flush request (`XLogFlush`) by
   storing its LSN and sleeping on a condition variable.
2. The first backend to register becomes the **leader**.
3. The leader calls `XLogWrite` which does the actual fdatasync.
4. After the I/O completes, the leader wakes all waiting backends.

This batches multiple commits into a single fdatasync, reducing
the fsync rate from N-per-commit to ~1-per-batch.

goopg's `FlushUpTo` is per-backend and per-call. There is no
batching. Each FlushUpTo potentially triggers an fdatasync.

### CRC-32C Hardware Acceleration

PostgreSQL uses CRC-32C (Castagnoli polynomial) for WAL record
checksums, not CRC-32 (IEEE). CRC-32C is supported by the Intel
`SSE4.2` CRC32 instruction, making it 5–10× faster than the
software-only CRC-32 that goopg uses. Go's `hash/crc32` package
uses a hardware-accelerated path when available
(`crc32_q` for IEEE on x86), but PostgreSQL explicitly chose
CRC-32C for its CPU instruction support.

### WAL Level

PostgreSQL supports multiple `wal_level` settings:

- `minimal` — enough for crash recovery (no replication).
- `replica` — supports physical replication.
- `logical` — supports logical replication (adds WAL records
  for individual row changes).

goopg always writes a full logical WAL stream (equivalent to
`wal_level = logical`).

### Full-Page Images (FPI)

On the first modification of a page after a checkpoint, PostgreSQL
writes a full-page image into the WAL. On recovery, the FPI is
used to reconstruct the page completely, ensuring crash safety
even if the previous page content was lost.

goopg also writes FPIs via the `logFPI` hook.

## goopg Improvement Analysis

### P0: Partitioned WAL Insert Lock

Replace `appendMu` with a partitioned lock. Each partition has
its own mutex. Backends acquire a partition based on
`runtime.fastrand() % N`.

```go
const walInsertPartitions = 16
var walInsertLocks [walInsertPartitions]sync.Mutex

func insertLock() func() {
    p := int(fastrand() % walInsertPartitions)
    walInsertLocks[p].Lock()
    return func() { walInsertLocks[p].Unlock() }
}
```

**Impact:** Up to 16× concurrency for WAL record insertion.
Estimated 10–20% improvement for high-TPS workloads.

### P1: Group Commit

Add a group-commit protocol to `FlushUpTo`:

1. Register the requested LSN in a shared list.
2. Try to become the leader (CAS on a leader flag).
3. If leader: perform fdatasync, then wake all registered
   waiters whose LSN ≤ completed LSN.
4. If not leader: wait on a condition variable.

**Impact:** Reduces fdatasync frequency from N-per-commit to
~1 per batch. Estimated 2–5× improvement for workloads where
fdatasync is the bottleneck.

### P2: CRC-32C

Switch from CRC-32 (IEEE) to CRC-32C (Castagnoli). Use the `go`
runtime's `crc32/castagnoli` table or the hardware instruction
when available.

**Impact:** Hardware-accelerated CRC on modern x86 CPUs. Estimated
10–20% reduction in WAL record encoding time.

## References

- goopg: `internal/wal/writer.go`
- goopg: `internal/wal/format.go`
- PG WAL insert: `postgres/src/backend/access/transam/xlog.c`
  (`XLogInsertRecord`, `XLogFlush`)
- PG WAL buffer: `postgres/src/backend/access/transam/xlog.c`
  (`WALInsertLock`, `WALInsertLockRelease`)
- PG group commit: `postgres/src/backend/access/transam/xlog.c`
  (`XLogFlush`, `WaitXLogInsertionsToFinish`)
