# Milestone 0042 — Align goopg's I/O subsystem with upstream PostgreSQL

**Status:** planned
**Depends on:** M0010 (WAL direct-I/O path that this milestone partly
unwinds), M0013 (WAL buffer + eviction-safe WAL-before-data
ordering), M0026 (concurrent WAL append architecture), M0024
(wait-event recording — preserved through the refactor).
**Drives:** Make goopg's data-file I/O, WAL I/O, WAL buffer, WAL
writer, and client-backend goroutine model match how upstream
PostgreSQL does it. Specifically: drop direct-I/O code paths in
favour of buffered I/O on the OS page cache (mirroring upstream's
default `O_DIRECT=off`), and tighten the WAL buffer / WAL writer /
client backend interaction so a goopg session reads the same way
a PG session does.

## 1. Context

After M0041 closed TPC-H result parity, the next gap between
goopg and upstream is structural: goopg ships an optional
`wal_direct_io` / `AlignedIO` path (M0010 + storage `setDirectIOIfRequested`)
that bypasses the OS page cache. PostgreSQL's default — and
overwhelmingly the deployed reality — is buffered I/O on top of
the kernel page cache, with `fsync` / `fdatasync` providing
durability boundaries instead of `O_DIRECT`. Aligning goopg with
the buffered model removes a significant chunk of platform-specific
code (Linux-only direct-I/O probes, alignment scratch buffers,
tail-RMW write loops) and makes the WAL writer / client backend
boundaries simpler to reason about.

This milestone has two halves:

1. **Survey** — produce an English design doc explaining how
   PostgreSQL does WAL writes / page reads-and-writes / victim
   eviction / background writer / checkpointer / WAL buffer /
   WAL writer. The output is a reference document, not a
   pre-existing TODO list. It will become the basis for any
   future I/O work.

2. **Refactor** — based on the survey, change goopg where the
   buffered model is a strict simplification:
   - Remove direct-I/O code paths in WAL and storage. Use plain
     buffered writes + `fdatasync` boundaries throughout.
   - Tighten the WAL buffer ↔ WAL writer ↔ client backend
     interaction so the per-connection goroutine model behaves
     like upstream's per-backend process model.

## 2. Required Design Docs

1. `docs/design/0042-0001-pg-io-survey.md` — **English** survey
   doc covering each upstream area listed in §3.1 below. The
   intent is reference quality, not a fix list.
2. `docs/design/0042-0002-buffered-io-migration.md` — replace
   `O_DIRECT` / `O_DSYNC` paths in `internal/wal/writer.go`
   (`writeAtDirectIO`, `enableDirectIO`, `direct_io_*.go`) and
   `internal/storage/smgr.go` (`setDirectIOIfRequested`) with
   plain buffered I/O. Retire the `wal_direct_io` GUC and the
   `Manager.AlignedIO` toggle. Keep alignment-aware buffer
   allocation only for the WAL ring (no functional change).
3. `docs/design/0042-0003-wal-buffer-and-writer-alignment.md` —
   align the WAL buffer / WAL writer goroutine roles with
   upstream:
   - Background WAL writer (`walwriter`-equivalent) drains the
     buffer on a timer and on flush requests; client backends
     no longer drive the drain themselves except at synchronous
     commit.
   - Synchronous-commit clients block on the writer's progress
     LSN, mirroring `XLogFlush` in upstream.
   - Buffer slot exclusion mirrors upstream's `WALInsertLock`
     partitioning where the goroutine model permits.
4. `docs/design/0042-0004-client-backend-goroutine-alignment.md`
   — document and (where straightforward) tighten the per-
   connection goroutine's role to match upstream's per-backend
   process: the goroutine owns its own snapshot, BufferPin
   waits, transaction-marker WAL append, and commit-time WAL
   flush. Background writer + checkpointer interactions are
   purely event-driven, not piggy-backed on the client goroutine.

The survey doc (0042-0001) lands first; the three refactor docs
(0002–0004) depend on it.

## 3. Definition of Done

### 3.1 Survey doc complete

`docs/design/0042-0001-pg-io-survey.md` covers, with citations
into `./postgres/src/backend/...`:

- WAL writes & durability — `XLogWrite`, `XLogFlush`,
  `XLogBackgroundFlush`, `issue_xlog_fsync` paths;
  `wal_sync_method`; `fdatasync` vs `fsync` choice; the
  insertion-lock array (`WALInsertLock`) and the wait-for-flush
  protocol.
- Page-data reads, writes, and durability — `BufferAlloc`,
  `BufferSync`, `FlushBuffer`; victim selection
  (`StrategyGetBuffer` / clock-sweep); the WAL-before-data
  invariant (`XLogFlush(LSN)` before page write).
- Background writer (`bgwriter.c`) — purpose, cadence,
  interaction with the dirty-page list, why it does NOT issue
  WAL fsyncs.
- Checkpointer (`checkpointer.c`) — what `CreateCheckPoint`
  persists, the buffer-flush phase, the WAL retention update,
  the relationship to `synchronous_commit` and recovery.
- WAL buffers (`xlog.c`, `xloginsert.c`) — ring layout, the
  `WALBufMappingLock`, the page-eviction-when-full path, the
  `XLogInsert` → `WALWriteLock` handoff.
- WAL writer (`walwriter.c`) — distinct from the background
  writer; flush cadence, idle behaviour, how it interacts with
  `synchronous_commit`-issuing backends.

### 3.2 Direct I/O removed

- `internal/wal/direct_io_linux.go`, `internal/wal/direct_io_other.go`,
  `writeAtDirectIO`, and `enableDirectIO` deleted.
- `internal/storage/direct_io_linux.go`, `direct_io_other.go`,
  `setDirectIOIfRequested` deleted.
- GUC `wal_direct_io` removed; `ManagerConfig.AlignedIO` removed
  or downgraded to a no-op deprecation.
- All callers updated; `golang.org/x/sys/unix` dependency for
  `O_DIRECT` removed (other unix deps may stay).
- `go test ./...` green; `make ralph-state-guard` passes.
- `TestTPCHResultParity` still **identical=22 divergent=0
  errored=0** (M0041's gate must not regress).

### 3.3 WAL buffer + WAL writer alignment

- A dedicated `walwriter` goroutine owns buffer drain on a
  timer (`wal_writer_delay`-shaped GUC, default 200ms);
  client backends drive drain only at synchronous commit, by
  waiting on a published `flushedLSN`.
- The WAL writer goroutine is documented in
  `docs/design/root-0005-buffer-manager.md` (or a successor
  doc) as the only goroutine that calls `writeAt`/`fdatasync`
  on a WAL segment.
- Synchronous-commit clients no longer enqueue
  `opAppend`+`opFlush` on every commit; they enqueue
  `opAppend` only and wait for `flushedLSN ≥ commitLSN`.

### 3.4 Client backend goroutine alignment

- Per-connection goroutine documented in
  `docs/design/0042-0004-client-backend-goroutine-alignment.md`
  with explicit mapping to upstream PG's per-backend process
  responsibilities.
- Background writer / checkpointer hooks invoked only by the
  background-writer / checkpointer goroutines, never by the
  client goroutine.

### 3.5 No new TPC-H regressions

- `TestRunTPCHQueriesAgainstSyntheticData` still 22/22.
- `TestTPCHResultParity` still identical=22 divergent=0
  errored=0.
- `make ralph-state-guard` passes for every loop touching
  the milestone.

## 4. Out of scope

- Async I/O / `io_uring` (M0009 territory).
- Walsender in-memory handoff (M0010-0002).
- Replacing the background writer with PostgreSQL's full
  `bgwriter.c` clock-sweep heuristics — a separate optimisation
  if it ever turns out to matter on goopg's workload.

## 5. Reference

- `postgres/src/backend/access/transam/xlog.c` — `XLogWrite`,
  `XLogFlush`, `XLogBackgroundFlush`, `issue_xlog_fsync`.
- `postgres/src/backend/access/transam/xloginsert.c` —
  `XLogInsert`, `WALInsertLock` handling.
- `postgres/src/backend/storage/buffer/bufmgr.c` — `BufferAlloc`,
  `BufferSync`, `FlushBuffer`, victim selection.
- `postgres/src/backend/postmaster/walwriter.c`,
  `bgwriter.c`, `checkpointer.c`.
- `internal/wal/writer.go`, `internal/wal/wal_buffer.go`,
  `internal/wal/checkpointer.go`,
  `internal/storage/smgr.go`, `internal/storage/bufpool.go`,
  `internal/server/dispatch.go`.
- Existing design docs to read alongside this milestone:
  `0010-0001-wal-direct-io-write-path.md`,
  `0013-0001-wal-buffers-architecture.md`,
  `0013-0002-overflow-and-eviction-durability-ordering.md`,
  `0026-concurrent-wal-append.md`.
