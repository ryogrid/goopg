# Processing Flow from WAL Generation to fdatasync Durability on Primary

> **perf-optimize3-dash S4 note (2026-07-13)**: with EmitCanonical off (default)
> the commit path no longer appends a canonical XLOG_XACT_COMMIT record, so the
> flush-wait endLSN is the native commit record's end.

This note summarizes the current goopg execution path from WAL generation to fdatasync-based durability, following the actual call order in code.

## Overview

1. Update paths call WAL record generation hooks.
2. WAL is appended via `wal.Writer.Append`.
3. On transaction commit, `FlushUpTo(endLSN)` is called.
4. `flushUpTo` writes WAL up to the target LSN into segment files.
5. On Linux, durability is enforced via `fdatasync(2)`.

## 1. WAL Generation Hook Wiring

During initialization, various update events (FPI, heap insert/delete, B-tree split/insert, HOT update, etc.) are encoded into WAL payloads, and closures are wired to call `walWriter.Append`.

- Implementation: [internal/initdb/open.go](internal/initdb/open.go#L258)
- Representative examples:
  - FPI: [internal/initdb/open.go](internal/initdb/open.go#L258)
  - B-tree split: [internal/initdb/open.go](internal/initdb/open.go#L273)
  - Heap insert: [internal/initdb/open.go](internal/initdb/open.go#L284)

## 2. WAL Append (Generation Phase)

WAL itself is appended through `Append`.

- Implementation: [internal/wal/writer.go](internal/wal/writer.go#L530)

Key points:

- When `wal_buffers` is enabled, the fast path (`tryAppend`) first appends into the in-memory WAL buffer.
- On buffer overflow (or other conditions), it falls back to a path that drains to WAL segment files.
- At this point, data is appended but not yet durable (i.e., no fdatasync yet).

## 3. SQL Commit Enters the Synchronous Commit Path

The SQL protocol path eventually calls `TxnMgr.Commit`.

- simple query: [internal/server/dispatch.go](internal/server/dispatch.go#L132)
- extended query: [internal/server/dispatch_extended.go](internal/server/dispatch_extended.go#L142)

`TxnMgr.Commit` proceeds into MVCC manager `finish(..., XactCommit)` and invokes the xact marker hook.

- Commit entry: [internal/mvcc/manager.go](internal/mvcc/manager.go#L148)
- Hook setup: [internal/initdb/open.go](internal/initdb/open.go#L449)

Inside this hook, commit performs:

1. `Append(EncodeXactCommit(xid))`
2. `FlushUpTo(endLSN)` for that record

- Append: [internal/initdb/open.go](internal/initdb/open.go#L459)
- FlushUpTo: [internal/initdb/open.go](internal/initdb/open.go#L468)

So the current behavior is effectively equivalent to `synchronous_commit=on`.

## 4. From FlushUpTo to fdatasync

`FlushUpTo` is implemented by `flushUpTo`.

- API: [internal/wal/writer.go](internal/wal/writer.go#L557)
- Core logic: [internal/wal/writer.go](internal/wal/writer.go#L1081)

Main `flushUpTo(lsn)` steps:

1. Validate target LSN.
2. If needed, drain undrained bytes in `walBuf` to segment files up to target LSN (Stage 1).
3. Collect dirty segments up to the target LSN.
4. Run `dataSync` on each of those segments (Stage 2).
5. Update `flushedLSN = lsn`.

Actual segment writing is done by `writeAt`:

- [internal/wal/writer.go](internal/wal/writer.go#L1188)

## 5. Linux Durability Primitive (fdatasync)

On Linux builds, `dataSync` directly calls `unix.Fdatasync`.

- Implementation: [internal/wal/sync_linux.go](internal/wal/sync_linux.go#L18)

Therefore, on the primary commit path, commit returns successfully only after fdatasync succeeds up to the commit record `endLSN`.

## 6. WAL-before-data Guarantee (Supplement)

Before flushing data pages, WAL is flushed up to each page LSN.

- Per-page flush: [internal/storage/bufpool.go](internal/storage/bufpool.go#L1104)
- Batch flush: [internal/storage/bufpool.go](internal/storage/bufpool.go#L1033)

So the write-ahead rule is preserved: corresponding WAL is made durable before page data writeback.

## 7. Background walwriter Loop

The background walwriter periodically calls `FlushUpTo(walWriter.WrittenLSN())`
every `WalWriterDelay` (default 200 ms), draining and fsyncing any WAL buffered
since the last flush. `WrittenLSN()` is a lock-free read of the current write
position, so the target is always in range (an earlier revision passed the
sentinel `^uint64(0)`, which `flushUpTo` rejected; that bug is fixed).

- Call site: [internal/initdb/open.go](internal/initdb/open.go) — the
  `WalWriterDelay` ticker loop, `FlushUpTo(walWriter.WrittenLSN())`.

Since M0107 fix-03, `Writer.FlushUpTo` also takes a **pre-enqueue fast exit**:
when the requested LSN is already durable (`lsn <= flushedLSNAtomic`), it
returns immediately without touching the group-flush queue or the writer
goroutine — the goopg analog of PostgreSQL's `record <= LogwrtResult.Flush`
early return in `XLogFlush`. Combined with the background pre-flush above, a
commit whose LSN the walwriter already synced skips WAL I/O and all
coordination.

- Fast exit: [internal/wal/writer.go](internal/wal/writer.go) —
  `FlushUpTo`, `if lsn <= w.flushedLSNAtomic.Load()`.
