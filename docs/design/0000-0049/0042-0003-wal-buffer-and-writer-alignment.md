# 0042-0003 — WAL buffer + WAL writer goroutine alignment

**Status:** accepted (Phase 1 landed 2026-05-04)
**Parent milestone:** M0042
**Depends on:** `0042-0001-pg-io-survey.md`
**Date:** 2026-05-04

## 1. Objective

Align goopg's WAL ring + WAL writer goroutine with upstream
PostgreSQL's WAL buffers + `walwriter` process semantics:

- A dedicated `walwriter` goroutine drains the WAL ring on a
  timer (`wal_writer_delay`-shaped GUC) and on explicit flush
  requests.
- Client-backend goroutines doing **synchronous commit** wait on
  the writer's published `flushedLSN`, instead of driving the
  drain themselves.
- WAL ring page eviction (when an inserter wants a page that's
  not yet drained) blocks on the writer rather than performing
  the drain inline.
- Insertion-lock parallelism analogous to upstream's
  `WALInsertLock[]` lets multiple backends append concurrently.

The current goopg code (`internal/wal/writer.go`) serialises all
Append/Flush/Recycle ops through one writer goroutine, but
client backends drive it via channel sends and (effectively)
wait for the loop to process their flush op. The shape is close
to upstream; this doc tightens the model so the public API
matches upstream's `XLogInsert` / `XLogFlush` /
`XLogBackgroundFlush` triplet.

## 2. Design

### 2.1 Background WAL writer goroutine

A new goroutine launched at startup, `walwriterLoop`, runs:

```
for {
    select {
    case <-ticker.C:        // wal_writer_delay (default 200ms)
        backgroundFlush()
    case <-flushReq:        // a client requested explicit flush
        explicitFlush(req.lsn)
    case <-shutdown:
        return
    }
}
```

`backgroundFlush()`:
1. Drain any complete WAL pages from the ring to the segment
   file via `writeAt` (advance `LogwrtResult.Write` analogue).
2. If `Write - Flush ≥ wal_writer_flush_after` (default 1 MiB),
   issue `fdatasync` (advance `LogwrtResult.Flush` analogue).

`explicitFlush(lsn)`:
1. If `Write < lsn`, drain ring up to `lsn`.
2. `fdatasync` segment(s) covering the range.
3. Publish the new `flushedLSN`; wake any waiters via
   `sync.Cond` or a per-LSN waiter table.

### 2.2 Public WAL API rebind

```
XLogInsert(rec) → LSN
    Acquire one insertion-lock slot (round-robin).
    memcpy record bytes into ring at the next free position.
    Release insertion-lock slot.
    Return assigned LSN.

XLogFlush(lsn)        // synchronous-commit path
    if flushedLSN >= lsn { return }
    enqueue flush request for lsn
    wait on cond.Wait() until flushedLSN >= lsn
    return

XLogBackgroundFlush()  // walwriter only — internal
    drain ring; opportunistic fsync per §2.1
```

Client backends in their commit path call `XLogFlush(commitLSN)`
and that's it. They do NOT call `writeAt` themselves; they do
NOT call `fdatasync` themselves; they do NOT recycle segments.

### 2.3 Insertion-lock parallelism

`NumWALInsertLocks` (default 8) in-memory slots. Each slot is a
mutex + a `currentInsertLSN` published value. `XLogInsert`
picks a slot (e.g. round-robin or hash of GoroutineID) and
holds it while computing record size, reserving ring space, and
copying bytes. The writer goroutine, when draining, scans all
slots' `currentInsertLSN` to compute the safe-to-drain LSN
(only complete records).

This gives N-way parallel inserts for free — a meaningful win
in the goroutine model where backends are cheap.

### 2.4 Ring-page eviction

When an inserter advances the write head and the new page is
not yet drained, the inserter:

1. Issues an explicit flush request for the page's first LSN.
2. Sleeps on the writer's published `writtenLSN` ≥ that LSN
   (note: `writtenLSN`, not `flushedLSN` — page eviction needs
   the bytes off the ring, not on disk).
3. Resumes its insert.

Critically, the inserter does NOT do the write itself — that
keeps the writer goroutine the sole owner of `writeAt` calls,
matching upstream's `WALWriteLock` ownership.

### 2.5 GUC surface

- `wal_writer_delay` — default 200ms. Already implied by
  M0026; this milestone formalises it.
- `wal_writer_flush_after` — default 1 MiB. New.
- `wal_buffers` — already exists (M0013-0001); semantics
  unchanged.
- `synchronous_commit` — boolean (off skips
  `XLogFlush(commitLSN)`; on calls it). Currently goopg
  always behaves as `on`; this milestone keeps that default
  and adds the GUC for compatibility but does NOT need to
  expose `off` in the first cut.

### 2.6 What stays

- The single-goroutine ownership of `writeAt` /
  `fdatasync` / `RemoveOldSegments` — that's exactly upstream's
  `WALWriteLock` + walwriter pattern, just expressed as
  goroutine ownership instead of a shared lock.
- The eviction-safe WAL-before-data ordering from M0013-0002:
  buffer-pool `evictLocked` calls `wal.FlushUpTo(pageLSN)`
  before `mgr.WriteBlock`. After this milestone, that call
  becomes `XLogFlush(pageLSN)`.

## 3. What this changes (files)

- `internal/wal/writer.go`:
  - Split the existing serialiser loop into "background writer
    cycle" (timer-driven) + "explicit flush request" (channel-
    driven) handlers.
  - Public `Append` becomes `XLogInsert` shape (returns LSN);
    `FlushUpTo` becomes `XLogFlush` blocking on
    `flushedLSN >= lsn`.
  - Add insertion-lock array (8 slots) for concurrent
    `XLogInsert`.
- `internal/wal/wal_buffer.go`:
  - Track `writtenLSN` (drained from ring) separately from
    `flushedLSN` (durable).
  - Page-eviction wait blocks on `writtenLSN`.
- `internal/wal/checkpointer.go`:
  - Calls `XLogFlush(checkpointLSN)` instead of poking the
    writer loop directly.
- `internal/storage/bufpool.go`:
  - `evictLocked` calls `XLogFlush(pageLSN)` (renamed from
    `FlushUpTo`).
- `internal/server/dispatch.go`:
  - On `synchronous_commit = on`, `Commit()` calls
    `XLogFlush(commitLSN)` after writing the commit record.
- `internal/config/defaults.go`:
  - New: `wal_writer_delay`, `wal_writer_flush_after`,
    `synchronous_commit`.

## 4. What this preserves

- `TestTPCHResultParity` — identical=22 divergent=0 errored=0.
- All M0026 / M0013 invariants (eviction-safe ordering,
  recycle-after-checkpoint, etc.).
- Existing wait-event names (`WALSync`, `WALWrite`) stay; the
  emitting goroutine is now always `walwriter`, never a client
  backend (which is closer to upstream's reality where
  `XLogFlush` has clients waiting on the writer most of the
  time).

## 5. Verification

- `go test ./internal/wal/... -count=1 -race` — green.
- `go test ./internal/testutil/tpch -run TestTPCHResultParity`
  — identical=22 divergent=0 errored=0.
- `go test ./...` — green except pre-existing `tmp/`.
- pgbench-style smoke (manual) — synchronous-commit path stays
  durable across kill -9 of the goopg process.

## 6. Reference

- `docs/design/0042-0001-pg-io-survey.md` §2, §6, §7.
- `postgres/src/backend/access/transam/xlog.c` —
  `XLogWrite`, `XLogFlush`, `XLogBackgroundFlush`.
- `postgres/src/backend/postmaster/walwriter.c`.
- Existing goopg code: `internal/wal/writer.go`,
  `internal/wal/wal_buffer.go`,
  `internal/storage/bufpool.go`.
