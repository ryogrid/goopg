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

Initial loop scope focused on writer + WAL-before-data ordering; a
follow-up loop now adds minimal crash replay that applies WAL records
up to the last checkpoint marker.

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

### Checkpointer goroutine (minimal)

`internal/wal` now includes a minimal checkpointer worker:

- tick on configurable interval
- call `Pool.FlushAll()` through a `DirtyPageFlusher` interface
- append a `RecordKindCheckpoint` marker on successful flush
- call `FlushUpTo(checkpointLSN)` so the marker is durable

If page flush fails, no checkpoint marker is written for that tick; the
worker logs and retries on the next interval.

### Recovery model (minimal)

Recovery reads WAL records and replays only records up to the most
recent checkpoint marker (`RecordKindCheckpoint`). If no checkpoint
exists, recovery replays all records.

- `RecordKindPageImage`: full-page image redo record.
- `RecordKindCheckpoint`: consistency boundary marker.

This gives a conservative crash-recovery behavior that avoids applying
tail records beyond the last known consistent point.

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
- Crash replay is available with checkpoint-bounded semantics and can be
  extended by later loops with richer record types.
