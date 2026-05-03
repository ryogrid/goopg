# 0042-0004 — Client backend goroutine alignment

**Status:** draft
**Parent milestone:** M0042
**Depends on:** `0042-0001-pg-io-survey.md`,
`0042-0003-wal-buffer-and-writer-alignment.md`
**Date:** 2026-05-04

## 1. Objective

Document and tighten the role of goopg's per-connection goroutine
so it matches upstream PostgreSQL's per-backend process exactly:

- One goroutine per client TCP connection.
- That goroutine owns: the connection's transaction state, the
  active snapshot, the pinned-buffer set, all `XLogInsert`
  calls, and the `XLogFlush` at synchronous commit.
- That goroutine does NOT own: the WAL writer cycle, the
  background page writer cycle, the checkpointer cycle, or the
  walsender cycle. Those are independent goroutines;
  client-backend goroutines never run their loops by
  side-effect.

The current goopg server (`internal/server/server.go` +
`internal/server/dispatch.go`) is already shaped this way for
the most part; this milestone makes the boundary explicit and
removes any leftover places where a client goroutine drives
background work directly.

## 2. Mapping upstream → goopg

| Upstream concept | goopg analogue |
|---|---|
| `BackendStartup` fork | `srv.handleConn` goroutine spawn (`internal/server/server.go`) |
| `MyProc` slot in procarray | `BackendID` + `pg_stat_activity` registry entry (M0022) |
| `MyProc` snapshot | per-goroutine `mvcc.Snapshot` cached in `executor.Context` |
| `MyXact` transaction state | `txn.Manager` per-goroutine handle |
| Per-backend pin counts | `Pool.Pin/Unpin` reference counts on `Slot` (`internal/storage/bufpool.go`) |
| `XLogInsert` (per-backend) | `wal.Writer.XLogInsert` (after M0042-0003) — runs on the client goroutine |
| `XLogFlush` (synchronous commit) | `wal.Writer.XLogFlush(commitLSN)` — runs on the client goroutine, blocks on writer |
| `bgwriter` (separate process) | `pageWriterLoop` goroutine (new, optional first cut) |
| `walwriter` (separate process) | `walwriterLoop` goroutine (M0042-0003) |
| `checkpointer` (separate process) | `checkpointer.Loop` goroutine (already exists) |
| `walsender` (per replica) | `walsender.Run` goroutine (already exists) |

## 3. Invariants to enforce

1. A client goroutine never calls `Pool.FlushAll` /
   `Pool.FlushAllPaced`. Those are checkpointer-only.
2. A client goroutine never calls `wal.Writer.writeAt` or
   `fdatasync` directly. After M0042-0003 the only goroutine
   that does is `walwriter`.
3. A client goroutine never recycles WAL segments. Only the
   writer goroutine does, and only at the checkpointer's
   request.
4. A client goroutine never spawns a per-statement goroutine
   that outlives the statement. (Subqueries / parallel scans
   would change this; out of scope here.)
5. The wait-event recorded for synchronous-commit waits is
   `WALSync` (existing name from M0024) and is published by
   the client goroutine via `OnWALSync` hooks before sleeping
   on the writer's cond.

## 4. What this changes (files)

- `internal/server/dispatch.go`:
  - Remove any in-flight call to `Pool.FlushAllPaced` or
    `wal.Writer.writeAt` (current code already does not call
    those; this milestone documents and asserts the
    invariant).
  - On `Commit()`, call `wal.Writer.XLogFlush(commitLSN)`
    when `synchronous_commit = on` (default).
- `internal/server/server.go`:
  - Document the per-connection goroutine model in package
    comments, citing this design doc.
- `internal/storage/bufpool.go`:
  - Add a panic / dev-mode assert that `FlushAll` /
    `FlushAllPaced` are called only from the checkpointer
    goroutine. Easiest mechanism: pass a goroutine-id token
    into the API at construction time.
- (Optional first cut) `internal/storage/bgwriter.go`:
  - New `pageWriterLoop` goroutine that walks the buffer pool
    ahead of the eviction pointer and pre-flushes dirty pages
    on a timer (`bgwriter_delay`, default 200ms). This is
    NOT required for parity but matches upstream's role
    breakdown. If skipped in this milestone, leave a TODO
    referencing `0042-0001` §4.

## 5. What this preserves

- All wait-event correctness from M0024 (`pg_stat_activity`
  shows the right state for the right goroutine).
- All test expectations under
  `internal/testutil/cluster/...`.
- TPC-H parity: `TestTPCHResultParity` still
  identical=22 divergent=0 errored=0.

## 6. Verification

- `go test ./internal/server/... ./internal/storage/...
  ./internal/wal/... -count=1 -race` — green.
- Add a `TestBackendGoroutineDoesNotFsync` regression test
  that wraps `Pool.FlushAllPaced` / `wal.Writer.writeAt` in
  a goroutine-id check; the test runs a query and asserts
  the check never fires from a client goroutine.
- `make ralph-state-guard` passes.

## 7. Reference

- `docs/design/0042-0001-pg-io-survey.md` §8.
- `docs/design/0042-0003-wal-buffer-and-writer-alignment.md`.
- `postgres/src/backend/postmaster/postmaster.c` (server
  loop), `postgres/src/backend/tcop/postgres.c`
  (`PostgresMain`).
- goopg code: `internal/server/server.go`,
  `internal/server/dispatch.go`,
  `internal/storage/bufpool.go`.
