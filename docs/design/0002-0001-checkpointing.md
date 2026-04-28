# Checkpointing (Milestone 0002)

| Field       | Value                          |
| ----------- | ------------------------------ |
| Status      | accepted                       |
| Date        | 2026-04-28                     |
| Milestone   | 0002 — Production-Grade Checkpointing & Concurrent B-tree |
| Refines     | [root-0008-wal-and-recovery.md](root-0008-wal-and-recovery.md) |
| Supersedes  | —                              |

## Problem

Milestone 0001 brought up a checkpointer good enough for `pgbench`: a
goroutine that, every 10 seconds, called `Pool.FlushAll()` and then
appended one `RecordKindCheckpoint` marker. That gave us crash-recovery
correctness against a clean shutdown but skipped the production-grade
machinery upstream relies on:

- The cadence was hard-coded — no `checkpoint_timeout` GUC.
- Writeback went out as one synchronous burst — every dirty page hit
  the disk back-to-back, ignoring `checkpoint_completion_target`.
- Only the timer triggered checkpoints — `max_wal_size` (the volume
  trigger) and the `CHECKPOINT` SQL verb were both unimplemented.
- Pages mutated after a checkpoint were not protected against torn
  writes — there were no full-page-image WAL records.

This doc covers the design and trade-offs for the M0002 changes that
close those gaps. Concurrent B-tree work has its own doc; the two
milestones share the WAL infrastructure but are independent landings.

## Upstream reference

Primary sources:

- `postgres/src/backend/postmaster/checkpointer.c` — the
  checkpointer process, request queue, and timing wheel.
- `postgres/src/backend/access/transam/xlog.c` —
  `CreateCheckPoint`, `XLogCtl->checkPointInProgress`, full-page-image
  decisions in `XLogInsertRecord`.
- `postgres/src/backend/storage/buffer/bufmgr.c` —
  `BufferSync`, `SyncOneBuffer`, the for-loop over `BufferDescriptors`
  that the checkpoint paces with `CheckpointWriteDelay`.
- `postgres/src/backend/utils/misc/guc_tables.c` — defaults, units,
  and contexts for `checkpoint_timeout`,
  `checkpoint_completion_target`, `max_wal_size`, `min_wal_size`,
  `full_page_writes`.

goopg differs from upstream's process model (one Go goroutine instead
of an auxiliary process), but the **observable semantics** of the GUCs
match upstream so an operator's mental model carries over.

## Components added in this milestone

```
┌──────────────────────────┐    ┌────────────────────────┐
│ goopg start              │    │ pg_hba / postgresql    │
│   (cmd/goopg/main.go)    │◄───┤   .conf                │
└──────────┬───────────────┘    └────────────────────────┘
           │ reads GUCs (checkpoint_timeout,
           │   checkpoint_completion_target,
           │   max_wal_size, full_page_writes)
           ▼
┌──────────────────────────┐
│ wal.Checkpointer         │
│   • Run() loop           │   timer + volume + SQL CHECKPOINT
│   • CheckpointNow()      │
│   • SetInterval()        │
│   • SetMaxWALBytes()     │
│   • SetCompletionTarget()│
└──────┬─────────┬─────────┘
       │         │
       │         └────► volumeReporter (wal.Writer.WrittenLSN)
       │
       │ flushes via
       ▼
┌──────────────────────────┐
│ storage.Pool             │
│   • FlushAllPaced(pacer) │   spread-paced writeback
│   • FlushAll()           │   IMMEDIATE-speed fallback
│   • SetFullPageWrites()  │
│   • ResetCheckpointEpoch │   called after each successful checkpoint
│   • LogPageImage hook    │   FPI on first dirty per epoch
└──────────────────────────┘
```

## Decisions

### 1. GUC surface mirrors upstream

Five new GUCs were registered in `internal/config.BuildDefaultRegistry`:

| Name                            | Type    | Unit | Default | Range          | Context     |
| ------------------------------- | ------- | ---- | ------- | -------------- | ----------- |
| `checkpoint_timeout`            | int     | s    | 300     | [30, 86400]    | PGC_SIGHUP  |
| `checkpoint_completion_target`  | real    | —    | 0.9     | [0.0, 1.0]     | PGC_SIGHUP  |
| `max_wal_size`                  | int     | MB   | 1024    | [2, 2147483647]| PGC_SIGHUP  |
| `min_wal_size`                  | int     | MB   | 80      | [2, 2147483647]| PGC_SIGHUP  |
| `full_page_writes`              | bool    | —    | on      | —              | PGC_SIGHUP  |

`min_wal_size` is registered for SHOW/SET parity but doesn't yet drive
WAL-segment recycling (recycling itself is a follow-up; the goopg WAL
writer currently appends without size capping).

`goopg start` reads the registry once at boot and pushes the values into
the long-lived `wal.Checkpointer` and `storage.Pool` via the setters
listed above. A future loop will hook the control-socket RELOAD path
to re-apply the same setters when the file changes; today the
PGC_SIGHUP context is only an admission gate, not a live-update.

### 2. SQL `CHECKPOINT` runs at IMMEDIATE speed

Upstream's `CHECKPOINT` verb passes `CHECKPOINT_IMMEDIATE` so the
checkpointer skips its pacing logic. goopg follows suit:
`Checkpointer.CheckpointNow()` invokes `runCheckpoint(ctx, spread=false)`,
which uses the unpaced `flusher.FlushAll()` path. The same applies to
the `goopg ctl checkpoint` subcommand once that lands.

The wire-layer command tag is `CHECKPOINT`; planner produces a
dedicated `Checkpoint` plan node (not a `Utility` wrapper) because the
side-effects are real and the executor needs a typed channel into the
checkpointer.

### 3. Volume trigger uses an atomic LSN mirror

`wal.Writer` exposes `WrittenLSN()` backed by an `atomic.Uint64`
that is stored after each successful append inside the writer
goroutine. The checkpointer polls it once per second by default and
fires `runCheckpoint(ctx, spread=false)` when
`(writtenLSN - lastCheckpointLSN) >= MaxWALBytes`. The volume trigger
is **also** IMMEDIATE-speed: it's a backpressure signal, not a
cadence knob.

The poll-not-push design avoids a callback/queue between the writer
and the checkpointer, which keeps the writer's hot path lock-free
(it just stores into an atomic). Polling at 1 Hz is fine —
checkpoints take far longer than that, and the threshold ranges from
"a few segments" upward.

The one-second poll is itself a `time.Ticker`. The Run loop multiplexes
over `ticker.C` (timeout), `volumeC` (poll), and `ctx.Done()`.

### 4. Spread checkpoints are deadline-driven, not delay-per-buffer

Upstream's `CheckpointWriteDelay` computes the desired progress
fraction from elapsed time and sleeps if writeback is ahead of the
target. goopg implements the same idea with a per-buffer pacer
callback handed to a new `storage.Pool.FlushAllPaced` API:

```go
func (p *Pool) FlushAllPaced(pacer func(progress float64) error) error
```

`progress` is `(i+1)/N` where `N` is the size of the dirty-set
snapshot taken at entry. The checkpointer constructs the pacer as:

```go
deadline := start + Interval * CompletionTarget * progress
sleep until deadline (or ctx.Done())
```

Why "deadline-driven" rather than "uniform delay between buffers":

- Robust to wall-clock jitter. A slow flush early in the cycle
  (e.g. a busy disk for one buffer) doesn't cascade extra delay
  onto later buffers — they catch up.
- Self-correcting if `N` is wrong. The dirty-set snapshot is taken
  once at entry; if MarkDirty fires during writeback, the next
  cycle absorbs those pages. The pacer only governs the snapshot.
- Clean shutdown via context. A SIGTERM during a paced cycle
  unblocks the next sleep, which returns ctx.Err() up to
  `runCheckpoint`, which propagates to `Run`.

The last buffer is special-cased: `progress == 1.0` returns
immediately without sleeping. There's no point waiting after the
final write.

### 5. Full-page-image emission lives in `MarkDirty`

**Update (Landing 3a follow-up):** v0 emits an FPI on **every**
`MarkDirty`, not just first-dirty-per-epoch. The original
once-per-epoch design assumed companion logical change records
(`heap_insert`, `btree_insert`, ...) capture the deltas between
checkpoints; v0 has no such records yet, so first-dirty-only FPI
silently drops every subsequent mutation on a page across crash
recovery — the M0002 crash-recovery test in
`internal/initdb/recovery_test.go` failed exactly that way before
we relaxed the gating. The `fpiSinceCheckpoint` flag is still
tracked per slot (checkpointer's epoch bookkeeping) but no
longer gates emission. WAL volume rose substantially —
`pgbench -i -s 1` grew from ~10 MB to ~1.6 GB. Logical change
records (which would restore the once-per-epoch optimisation)
are deferred to a post-M0002 milestone.

The torn-write window is between "mutator updates page in the buffer"
and "checkpointer/eviction writes the page to disk." Upstream protects
against it by emitting a full-page image (FPI) WAL record on the
first modification of a page after each checkpoint (when
`full_page_writes` is on). Crash recovery replays the FPI so a
half-written page is overwritten with a known-good image before
subsequent change records replay against it.

We hooked the same place: `Pool.MarkDirty` snapshots the page and
calls a `LogPageImage(rel, blk, page) (LSN, error)` callback the first
time per epoch each page is dirtied. The FPI's end LSN is stamped into
the page header so the existing flush-before-write ordering
(`flushSlot -> wal.FlushUpTo(pd_lsn)`) covers it without further
plumbing.

The "first time per epoch" bookkeeping lives on the `Slot` itself:
`fpiSinceCheckpoint bool`, guarded by the pool mutex, cleared across
all slots by `Pool.ResetCheckpointEpoch()` after each successful
checkpoint marker.

Two design points worth calling out:

**Why MarkDirty, not the call site of every mutation?** The buffer
pool already serves as the choke-point for "this page is now dirty."
Every mutator already calls MarkDirty under `Slot.Lock()`. Centralising
FPI here means we get coverage of heap inserts, UPDATE/DELETE xmax
stamps, btree splits, and future index AMs without re-auditing each
caller. Upstream pushes FPI down into `XLogInsert` for the same
reason — it's a chokepoint.

**Why import-cycle gymnastics with the callback?** `internal/wal`
already imports `internal/storage` for `RelFileNode`/`Page`/`BlockNumber`.
The reverse import would be a cycle. We solved it with a closure passed
through `PoolConfig.LogPageImage` from `initdb.Open`, which lives
outside the cycle and can pull in both packages.

**Error policy.** `MarkDirty` returns nothing; v0 logs FPI failures via
`Pool.logger` and continues. Upstream PANICs on `XLogInsert` failure.
Surfacing the error here would require changing `MarkDirty`'s
signature (14+ call sites across executor/btree/vacuum), so it's
deferred. The scenarios that hit this — disk full on the WAL device,
WAL writer closed while a mutation is in flight — are catastrophic
either way; logging gives an operator a breadcrumb and the next
checkpoint marker will fail too, which is an externally visible
signal.

`MarkDirtyWithLSN` (used by callers that already issued a WAL record
for the change) sets `fpiSinceCheckpoint = true` so the same epoch
doesn't double-WAL the page.

### 6. Structural seam: optional interfaces

`wal.Checkpointer.flusher` is typed `DirtyPageFlusher` (just
`FlushAll()`), but the runtime concrete type is `*storage.Pool`. Two
optional behaviours are tested at runtime via type assertion:

- `epochResetter` (provides `ResetCheckpointEpoch()`) — called after
  every successful checkpoint.
- `pacedFlusher` (provides `FlushAllPaced(pacer)`) — used when
  spread is enabled and the type satisfies the interface.

Tests construct simpler fakes that only implement the base
interfaces, so the optional-interface pattern keeps test setup small
without a config knob.

The same pattern applies to `wal.Writer.WrittenLSN` via the
`volumeReporter` interface inside the checkpointer.

## Recovery behavior

Recovery (`internal/wal/recovery.go`) was not changed in this
milestone. The replay path already understands `RecordKindPageImage`
and `RecordKindCheckpoint`, which is what the M0002 producer side
emits. The crash-recovery TAP test (`pgbench` + SIGKILL + restart) is
a follow-up; the design doc for that work will live alongside the
TAP utility-library doc under M0004.

## Out of scope (deferred)

- Restart points (replication standby checkpoint analogues). No
  standby support yet.
- Recycling old WAL segments per `min_wal_size`. The writer
  currently keeps everything; a follow-up loop will add segment
  recycling once `min_wal_size` becomes load-bearing.
- `pg_stat_bgwriter` / `pg_stat_checkpointer` virtual tables.
  Listed in the M0002 fix-plan but pending the catalog work to
  expose them through pg_class as queryable views.
- The `goopg ctl checkpoint` CLI subcommand. The SQL verb already
  exposes the trigger end-to-end; the CLI hook is a tiny wrapper on
  the control socket.
- Surfacing `MarkDirty` FPI errors. See §5.

## Cross-references

- Milestone definition:
  [`docs/milestones/0002-durability-and-concurrent-storage.md`](../milestones/0002-durability-and-concurrent-storage.md).
- Buffer pool internals:
  [root-0005-buffer-manager.md](root-0005-buffer-manager.md).
- WAL writer & recovery seam:
  [root-0008-wal-and-recovery.md](root-0008-wal-and-recovery.md).
- GUC system:
  [root-0004-configuration-and-guc.md](root-0004-configuration-and-guc.md).
