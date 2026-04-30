# REF-016: WAL Buffer & Eviction

…(existing content up to "Key Differences" unchanged)…

## PostgreSQL Implementation (Deep Dive)

### Lock-Free WAL Ring (XLogCtl)

PostgreSQL's WAL buffer is a fixed-size ring in shared memory
(`XLogCtl->xlblocks`). Each buffer slot is an atomic `uint64`
that encodes both the LSN of the first byte in the slot and a
"free/used" flag. Backends competing for a slot use a CAS
(compare-and-swap) loop on the atomic flag, not a mutex.

The ring has `XLOGbuffers` slots (default 16 MB / XLOG_BLCKSZ
≈ 1024 slots of 8 KB each). Multiple backends can simultaneously
copy records into different slots.

goopg's `walBuf` is protected by `appendMu` (a single mutex).
All backends contend on this one mutex for every WAL insert.

### WAL Writer Flush Interaction

PostgreSQL's WalWriterMain:
1. Wakes every `wal_writer_delay` (default 200 ms).
2. Flushes all WAL buffers that have been filled since the last
   wakeup.
3. Updates `shmem->WalWriterFlushLSN`.

Backends that call `XLogFlush` for a COMMIT check whether the
WalWriter has already flushed past the requested LSN. If so,
they return immediately without doing I/O.

goopg's state loop handles both writing AND flushing. There is
no dedicated WalWriter to pre-flush buffers.

### wal_buffers Sizing

PostgreSQL's `wal_buffers` defaults to 16 MB (or 1/32 of
`shared_buffers`, whichever is smaller). The buffer must be
large enough to absorb the WAL generated during the gap between
WalWriter wakeups.

goopg's `wal_buffers` defaults to 16 MB. The sizing logic does
not consider `shared_buffers`.

## goopg Improvement Analysis

### P1: Lock-Free Slot Allocation

Replace the single `appendMu` with atomic slot allocation into
the ring buffer.

```go
type walBufferSlot struct {
    flag atomic.Uint64  // 0 = free, 1 = reserved, 2 = full
    data [BlockSize]byte
}
```

Each backend atomically claims a slot, copies its record, and
sets the flag to "full". The state loop drains "full" slots.

**Impact:** Eliminates `appendMu` contention. Multiple backends
can insert WAL records simultaneously.

### P1: Dedicated WalWriter

Add a WalWriter goroutine that periodically (every 200 ms)
flushes dirty WAL buffers. `FlushUpTo` first checks whether
the WalWriter has already flushed past the requested LSN.

**Impact:** COMMIT latency is reduced because the WAL is already
flushed when FlushUpTo is called.

## References

- goopg: `internal/wal/wal_buffer.go`
- PG WAL buffer: `postgres/src/backend/access/transam/xlog.c`
  (`XLogInsertRecord`, `XLogWrite`, `WALInsertLock`)
- PG WalWriter: `postgres/src/backend/postmaster/walwriter.c`
