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

## PostgreSQL Implementation

PostgreSQL's WAL buffers (`xlog.c`) are a fixed-size ring in
shared memory (`XLogCtl->xlblocks`):

- **Insertion** — each backend copies its WAL record into the
  ring under the partitioned `WALInsertLock`. If the ring is full,
  the insertion waits for a WAL writer to drain.
- **WAL writer** — the dedicated `WalWriterMain` periodically
  flushes dirty buffers. Backends waiting for commit can also
  flush directly.
- **XLogFlush** — waits until `flushedUpto ≥ requestedLSN`.
  The WAL writer may perform the I/O; waiting backends sleep
  on a condition variable.
- **Group commit** — multiple backends waiting for the same
  flush point are grouped. The first to arrive performs the I/O;
  others are woken when it completes.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Buffer size | Fixed at startup (`WALBuffers`, default 16 MB) | Fixed at startup (`wal_buffers`, default 16 MB) |
| Insert concurrency | Single `appendMu` mutex | Partitioned `WALInsertLock` (16–64 partitions) |
| Overflow handling | Drain synchronously in state loop | Wait for WAL writer drain |
| Flush batching | None (per-FlushUpTo) | Group commit (XLogFlush) |
| Thread safety | `sync.Mutex` (M0026) | Lock-free ring with atomic slots |

## Potential Optimisations or Corrections

- **Partitioned insert lock** would let concurrent backends append
  to different buffer partitions, eliminating `appendMu` contention.
- **Group commit** would reduce fdatasync frequency by batching
  multiple commit flushes.

## References

- goopg: `internal/wal/wal_buffer.go`
- goopg: `internal/wal/writer.go` (drainBufferBytes, flushUpTo)
- PG WAL buffer: `postgres/src/backend/access/transam/xlog.c`
  (`XLogInsertRecord`, `XLogFlush`, `WALInsertLock`)
