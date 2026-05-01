# REF-016: WAL Buffer & Eviction

## Overview

The WAL buffer absorbs append operations in memory so that committing transactions do not block on disk I/O. Records accumulate in the buffer until either (a) the buffer fills up (overflow drain), or (b) a FlushUpTo call forces a drain + fdatasync.

## goopg Implementation

**Package:** `internal/wal/wal_buffer.go`

### Key Type

`walBuffer` is a fixed-size byte ring buffer addressed by absolute LSN:

```go
type walBuffer struct {
    mu   sync.Mutex  // M0026: thread-safe for concurrent append
    cap  int64
    buf  []byte      // ring backing store; len == cap
    base int64       // LSN that buf[0] currently represents
    head int64       // first un-drained byte LSN
    tail int64       // first unwritten byte LSN
}
```

### Buffer Operations

| Method | Description |
|--------|-------------|
| `append(record)` | Copy record bytes into the ring at position `tail`. Advances `tail`. |
| `readForDrain(n)` | Return up to `n` bytes from `head` for writing to a segment file. |
| `advanceHead(n)` | Advance `head` after the drained bytes have been written. |
| `free()` | Free space = `cap - resident()`. |
| `resident()` | Un-drained bytes = `tail - head`. |

### Drain Path

When the buffer is full (append would overflow) or FlushUpTo is called:

1. `drainBufferBytes(need, reason)` — reads bytes from `head` via
   `readForDrain`, writes them to the segment file via `writeAt`,
   then calls `advanceHead`. The drain runs in the state-loop
   goroutine.
2. After drain, the segment file is fsynced (if this was the
   terminal flush).

### Overflow vs Flush

| Trigger | Reason | Behaviour |
|---------|--------|-----------|
| Buffer full during append | `drainReasonOverflow` | Drain just enough to make room for the new record. |
| FlushUpTo called | `drainReasonFlush` | Drain everything up to the requested LSN, then fdatasync. |

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
