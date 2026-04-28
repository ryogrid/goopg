# 0008 — WAL Writer and Recovery Seam (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Milestone 5 requires write-ahead logging and crash recovery. The prior
storage work (`0005`, `0006`) already established the page header
`pd_lsn` field and dirty-page flush paths, but there was no WAL
implementation enforcing the rule "WAL must reach durable media before
data page writeback".

References into upstream:

- `postgres/src/backend/access/transam/xlog.c` — WAL insertion, flush,
  segment management, and recovery entry points.
- `postgres/src/include/access/xlog_internal.h` — WAL segment and LSN
  constants.
- `postgres/src/backend/storage/buffer/bufmgr.c` — data-page flush path
  with WAL-before-data ordering.

## Decision

### Scope of this loop

Land a **library-first WAL layer** and wire it into buffer flush paths:

1. `internal/wal` writer that serializes append/flush operations through
   one goroutine.
2. Segmented WAL files under `pg_wal/`.
3. `FlushUpTo(lsn)` contract that performs `fdatasync`-equivalent
   persistence for records up to `lsn`.
4. Buffer manager integration so every dirty page write asks WAL to
   flush up to that page's `pd_lsn` first.

Do not land full crash replay in this loop. Recovery implementation is
the next loop's item; this document defines the seam and invariants it
relies on.

### WAL stream model

The WAL stream uses a monotonic byte-position LSN model:

- LSN is 1-based byte position in the stream.
- `0` means "no WAL assigned".
- Append returns `[startLSN, endLSN]` for each record.

This keeps buffer/WAL integration simple because page `pd_lsn` can be
treated as "the last byte that must be durable before this page can be
written".

### Segment layout

- Directory: `pg_wal/`
- Segment size: default 16 MiB (configurable for tests)
- Segment names: 24-hex uppercase sequence numbers (`%024X`), starting
  at `000...000`.

Records may span segment boundaries. The writer handles splitting
transparently.

### Record format (v0)

Each record is a compact length-prefixed frame:

```
uint32 payload_length (little-endian)
uint32 crc32(payload)
payload bytes
```

This is intentionally simpler than upstream `XLogRecord` while the SQL
surface is still minimal. It provides corruption detection and a stable
replay primitive.

### Writer concurrency model

All WAL operations are serialized by one worker goroutine:

- `Append(payload)`
- `FlushUpTo(lsn)`
- `Close()`

Callers interact via request/response channels. This avoids lock
contention and enforces total order over writes and flushes.

### Buffer-manager integration

`internal/storage.Pool` gains an optional `WALFlusher` dependency:

```go
type WALFlusher interface {
    FlushUpTo(lsn uint64) error
}
```

Before writing any dirty page to data files (eviction or `FlushAll`),
the pool does:

1. Read `pd_lsn` from page header.
2. If `pd_lsn != 0`, call `FlushUpTo(pd_lsn)`.
3. Only then issue data-file write.

If WAL flush fails, data flush is aborted and the error propagates.

### Checkpointer seam

This loop keeps the existing `FlushAll()`-driven checkpoint seam:

- future checkpointer goroutine calls `Pool.FlushAll()` periodically
- WAL-before-data ordering is already enforced inside `FlushAll()`

Adding the actual scheduler goroutine is a thin follow-up once server
lifecycle wiring (`goopg start -D`) is ready.

## Alternatives considered

- **Write WAL directly from callers with mutexes.**
  Rejected: too easy to violate global ordering and harder to reason
  about flush latency under concurrency.
- **Implement full upstream `XLogRecord` now.**
  Rejected for loop size. v0 frame format is enough to prove durability
  and ordering; format evolution can be handled by a superseding doc.
- **Skip CRC in v0.**
  Rejected: recovery needs basic corruption detection from day one.

## Consequences

- Dirty-page writeback now enforces WAL-before-data.
- WAL durability can be tested independently from SQL transaction logic.
- Crash replay work can proceed in the next loop on top of a stable WAL
  append/flush/read foundation.
