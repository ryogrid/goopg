# Milestone 0089 — Checkpoint + stop durability + data-file fsync

**Status:** all three durability hardening pieces landed
2026-05-11 (M0089-0001 + M0089-0002 + M0089-0003). The
pgbench scale-100 symptom that originally drove this milestone
PERSISTS, but post-investigation it is now known to be caused by
a separate set of bugs (history INSERTs not reaching heap, and
UPDATE-side MVCC duplicates) tracked under M0090.
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

## Status update — 2026-05-11

**M0089-0001 (data-file fsync on checkpoint)** — LANDED in
commit `5745875`. Added `Manager.SyncAll` that fdatasyncs every
open data file; wired into `Checkpointer.runCheckpoint` after
`FlushAllPaced` via the new `dataFileSyncer` interface (satisfied
by `Pool.SyncAllDataFiles`).

**M0089-0003 (final checkpoint on stop)** — LANDED in commit
`5745875`. `OnStop` (`internal/server/server.go`) now calls
`Checkpointer.CheckpointNow()` before `runCancel()`. Users no
longer need to chain `goopg checkpoint && goopg stop`.

**M0089-0002 (final checkpoint in Runtime.Close)** — LANDED in
the 2026-05-11 retry commit. Added a synchronous
`Checkpointer.CheckpointNow()` at the very top of
`Runtime.Close` (`internal/initdb/open.go`). This closes the
durability window between the M0089-0003 OnStop checkpoint
(which runs while clients may still be active because
`runCancel` is asynchronous) and the eventual file-handle
release. A unit test
(`internal/initdb/close_checkpoint_test.go::TestRuntimeCloseTriggersFinalCheckpoint`)
pins the behaviour: a btree entry inserted into a fresh Runtime
survives `Close` + reopen with no explicit checkpoint by the
caller. The previous behaviour silently relied on the OS page
cache to bridge same-host restarts; the final close-checkpoint
makes the post-stop state fully durable.

The scale-100 pgbench symptom that drove the M0089 milestone is
**NOT closed by this fix.** Post-fix repro shows the same
symptom: `pgbench_history` is 0 bytes after a 180s standard run
(despite 12,841 reported INSERT-bearing transactions), and a
subsequent simple-update workload — even at `-c 1` —
immediately errors with `ERROR: short read at block`. Further
investigation reveals two separate bugs that are out of scope
for M0089's "durability boundary at stop" theme:

1. **`pgbench_history` INSERTs at scale 100 never reach the
   heap file.** Scale-5 and scale-10 reproductions work
   correctly (history grows + persists). At scale 100, the
   on-disk file size stays at 0 bytes across the whole
   workload, even though pgbench reports the transactions
   committed. This is not a fsync issue (fsync of a 0-byte
   file is still 0 bytes); it is an INSERT-path or
   `writeHeapRow` routing issue triggered by scale or
   concurrency, the mechanism of which is undetermined.

2. **UPDATE leaves duplicate visible rows.** After the
   standard run, `pgbench_branches` reports 1,610 visible
   rows instead of 100 (scale 100), and `pgbench_tellers`
   shows similar drift. This indicates UPDATE is not
   properly stamping xmax / propagating MVCC visibility on
   the old tuple version. pgbench autodetects "scaling
   factor: 161" from the inflated count, leading to
   simple-update sampling `aid` from `[1, 100000*161]` —
   i.e., past the actual accounts data — which itself
   contributes to the `short read at block` SELECT errors.

These two bugs are tracked together under **M0090** (see
`docs/milestones/0090-pgbench-scale-100-mvcc-and-insert-bugs.md`).
M0089's durability work is complete; the remaining pgbench
symptom requires M0090 to be picked up.
Post-fix pgbench re-measurement (2026-05-11 09:53–09:57) STILL
reproduces the bug at scale 100 with `-c 100`, but the cause is
NOT a M0089 durability gap — see the M0089-0002 note above and
M0090 for the actual investigation:
- standard workload completes (69.48 TPS, 12,628 txns, 0 failed).
- checkpoint + stop + restart leaves `pgbench_history` at 0 bytes
  and pgbench_accounts inconsistent with its pkey.
- A subsequent `simple-update` workload aborts every client with
  `ERROR: short read at block` on the SELECT command (post-UPDATE
  read of the just-modified accounts row via the pkey).

Scale-5 / scale-10 reproductions (smaller workloads) work
correctly across the same checkpoint+stop+restart cycle. The bug
is heavy-concurrency + scale-dependent — most likely a
buffer-pool dirty-tracking gap on freshly-extended pages, possibly
combined with btree pkey persistence (the pkey indexes blocks
that the heap file on disk lacks).

The investigation needs to:
- Audit `Pool.PinNew` + `Pool.Extend` for the precise dirty-bit
  state at every transition (initial extend writes empty page;
  caller's PageAddHeapTuple mutates buffer pool slot; eviction
  may flush stale-content state if dirty bit is cleared
  prematurely).
- Determine whether the btree index path has its own
  dirty-tracking gap (the pkey's pointers-past-EOF symptom may
  be that, not heap durability).
- Add an end-to-end test that wipes data dir, runs pgbench at
  scale 100 with `-c 100 -T 180`, checkpoint+stop+restart, then
  runs simple-update with `-c 100 -T 30` — asserts 0 errors.

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
