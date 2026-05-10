# Milestone 0089 — Checkpoint + stop durability + data-file fsync

**Status:** planned
**Depends on:** M0079 (catalog DDL WAL recovery), M0080 (heap WAL
parity + VM/FSM persistence)
**Drives:** clean stop+restart durability for write-heavy
workloads — `pgbench_history` should not lose committed INSERTs
across a checkpoint+stop+start cycle, and subsequent workloads
against the same data dir must not error with
`ERROR: short read at block`.

## Context

A pgbench run on 2026-05-11 (`-c 100 -j 100 -T 180`, scale 100,
12,628 transactions, 0 reported failures) followed by
`bin/goopg checkpoint && bin/goopg stop && bin/goopg start` left
`pgbench_history` with 0 rows in-memory AND a 0-byte heap file on
disk — even though every transaction's TPC-B-like script INSERTed
a row into history.

A subsequent `simple-update` workload against the same data dir
errored with:

```
pgbench: error: client N script 0 aborted in command 7 query 0:
  ERROR:  short read at block
```

The btree pkey was returning TIDs pointing past the heap file's
EOF — an index/heap inconsistency.

Three concrete gaps identified by code audit
(`internal/storage/bufpool.go:1168-1290`,
`internal/wal/checkpointer.go:272-325`,
`internal/storage/smgr.go:487-501`,
`internal/server/server.go:410`):

1. `Pool.FlushAllPaced` does pwrites then clears dirty bits but
   never calls `fsync`. `Checkpointer.runCheckpoint` syncs the
   WAL but not the data files. `Manager.Sync`
   (`internal/storage/smgr.go:321`) exists but is unwired.
2. `Pool.PinNew` / `Pool.Extend` dirty-bit tracking for
   newly-extended pages needs an audit — particularly the path
   where a fresh page is allocated, a tuple is inserted, and the
   page is later flushed by checkpoint vs eviction.
3. `bin/goopg stop` (`OnStop` → `runCancel`) does not trigger an
   implicit shutdown checkpoint. Users must chain
   `checkpoint && stop` manually; missing the prior checkpoint
   loses every post-last-checkpoint write that hasn't been flushed
   via the normal bgwriter cadence.

(The pure-fsync hypothesis alone does NOT fully explain the 0-byte
on-disk file — the OS page cache should satisfy a same-host
re-open even without fsync — so the dirty-tracking audit is the
likely-load-bearing piece of this milestone. See design 0089-0002.)

## Required design docs

- `docs/design/0089-0001-data-file-fsync-on-checkpoint.md` — wire
  `Manager.Sync` into `Checkpointer.runCheckpoint` after
  `FlushAllPaced` completes; performance trade-off discussion vs
  the current WAL-only sync mode.
- `docs/design/0089-0002-buffer-pool-dirty-tracking-on-extend.md`
  — audit `Pool.PinNew` + `Pool.Extend` for completeness of
  dirty-bit tracking on freshly-created pages; identify and fix
  any path where a newly-extended page's dirty bit is cleared
  prematurely.
- `docs/design/0089-0003-final-checkpoint-on-graceful-stop.md` —
  implicit shutdown checkpoint in `bin/goopg stop` so users don't
  have to chain `goopg checkpoint && goopg stop`.

## Tasks

Tasks will be detailed when this milestone is picked up. See the
fix_plan.md note about the milestone-only convention.

## Definition of Done (sketch)

- `pgbench_history` and other INSERT-target heaps persist their
  rows across a `checkpoint + stop + start` cycle.
- Subsequent workloads on the same data dir do not see
  `ERROR: short read at block`.
- `bin/goopg stop` (no prior checkpoint) is sufficient to
  preserve all committed writes.
- An end-to-end pgbench run (init → standard → simple-update →
  select-only, fresh restart per workload) completes with 0
  failed transactions across all three workloads.
- Tests:
  - checkpoint flushes + fsyncs data files (write a page, mark
    dirty, checkpoint, read file content directly via `os.ReadFile`,
    assert match).
  - buffer-pool dirty-tracking-on-extend (if audit found a bug):
    extend, write tuple, evict, reopen Manager, read page back,
    assert tuple bytes match.
  - server stop triggers implicit checkpoint (write data via
    session, send control-socket Stop, restart, query state,
    assert data present).
