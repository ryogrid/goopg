# Standby-side Recovery and Replay (M0005)

**Status**: accepted
**Milestone**: 0005 (Streaming Replication Support)
**Sibling**: `0005-0001-streaming-replication-architecture.md`,
            `0005-0003-replication-observability.md`

## Goal

Make a goopg standby process apply WAL records as they arrive from
the primary, not just on next-restart crash recovery. The standby
must end up with on-disk data files semantically identical to the
primary's, with bounded apply lag and idempotent restart behaviour.

## What changed

The pre-M0005 recovery path (`wal.ReplayRecords` /
`wal.ReplayFromDirWithMgr`) was batch-oriented: open the data
directory, scan all of `pg_wal`, find the last checkpoint, replay
the tail in one pass, then start serving. That model is correct for
crash recovery on a primary but wrong for a streaming standby:
records arrive indefinitely and must apply continuously, not in one
shot.

This loop lifts `ReplayRecords` into a per-record kernel
(`wal.ApplyRecord`) and adds a streaming driver
(`wal.StreamReplayer`) that consumes from a `wal.RecordIterator`
and applies each record as it lands.

## Components

### `wal.ApplyRecord(mgr, r) (applied bool, err error)`

The per-record dispatch kernel. `ReplayRecords` and
`StreamReplayer` both call this for each record. Returns
`applied=true` for records that mutate a page, `false` for marker
records (currently just `RecordKindCheckpoint`). All record kinds
are individually idempotent via `pd_lsn` — re-applying a record
that's already on disk is a silent no-op.

The function is a pure refactor of the switch that used to live
inside `ReplayRecords`. No semantic changes; existing crash-recovery
tests pass without modification.

### `wal.StreamReplayer`

The standby-side continuous-replay driver. Construct via
`NewStreamReplayer(mgr, baselineLSN)`, then call `Run(ctx, iter)`
from a goroutine. The replayer:

1. Pulls one record at a time from the iterator (which blocks at
   the writer's `WrittenLSN` tail and wakes on flush events).
2. Calls `ApplyRecord` on the record.
3. Advances its internal `applyLSN` cursor to `record.EndLSN`.
4. Counts records and applies for `Stats()`.

`Run` returns nil on context cancellation, on iterator EOF (the
writer closed), and on `wal.ErrClosed`. It returns an error only on
a per-record apply failure — see "Failure model" below.

`ApplyLSN()` is safe to call concurrently and returns the LSN of
the last record successfully applied (or the constructor-supplied
baseline if no records have been applied yet). Future loops will
expose this as `pg_stat_wal_receiver.applied_lsn` for observability.

### Wire-up: `cmd/goopg start` standby boot

When `<DataDir>/standby.signal` is present, `goopg start` now
spawns three coordinated background goroutines:

- The **walreceiver** (`server.WalReceiver`) — produces records by
  reading from the primary and `Append`'ing into the local WAL
  writer.
- The **stream replayer** (`wal.StreamReplayer`) — consumes from
  a `RecordIterator` rooted at the writer's `WrittenLSN` at boot
  time, and calls `ApplyRecord` on each record into
  `rt.StorageMgr`.
- The **checkpointer** (unchanged from M0002).

All three share a parent context that's cancelled on shutdown.
The replayer iterator's subscription to the writer's flush events
means there's no polling — record arrival on the primary wakes the
standby's apply loop within one round-trip plus one OS scheduler
tick.

The crash-recovery pass inside `initdb.Open` still runs once at
startup. That pass advances data files up to the durable WAL tail
that survived the previous shutdown; the iterator then continues
from exactly that LSN, so there's no double-apply window.

## Restart and resume

The contract is **no separate apply cursor**. We exploit two
properties:

1. WAL records on disk are the durable source of truth. The
   walreceiver fsyncs them via the standard writer flush path; once
   the writer's `WrittenLSN` has advanced, the record will be
   re-readable on next boot.
2. Every record kind's apply is idempotent via `pd_lsn` (see
   M0002, `docs/design/0002-0003-redo-records.md`). Re-applying a
   record whose effect is already on the page is a silent no-op.

So on restart:

1. `initdb.Open` runs the existing crash-recovery batch over the
   WAL on disk — applies anything that was on disk but not yet
   reflected in data files.
2. The standby boot path constructs a `RecordIterator` anchored at
   the (post-recovery) `WrittenLSN`.
3. The replayer applies new records arriving from the primary, plus
   any tail the iterator picks up if the primary streamed past
   `WrittenLSN` while crash-recovery was running. Idempotency
   covers the overlap.

This is exactly the same model upstream uses for "consistent
recovery point + replay continues from there", just expressed in
terms of LSN rather than checkpoint+ minRecoveryPoint state.
A future loop adds a `min_recovery_point` analogue for hot-standby
read consistency; v0's standby does not yet accept queries.

## Failure model

A `StreamReplayer.Run` error means a record decoded fine but its
apply hit storage trouble (file write failure, page-format
mismatch, an unsupported record kind). v0 treats this as
unrecoverable:

- The primary's WAL stream is by construction valid (the same code
  path produced and validated each record on the primary's side).
  An apply failure on the standby therefore signals storage
  divergence, not a transient network issue.
- Continuing to apply past such a divergence would compound the
  corruption into pages whose `pd_lsn` no longer matches reality.
- The recovery is operator-driven: take a fresh base backup, wipe
  the standby's data dir, restart.

The standby boot path therefore logs the apply error and exits the
replayer goroutine. The walreceiver keeps appending — preserving
the durable WAL stream for forensic analysis — but no further
records reach data files until the operator reboots.

A network failure on the walreceiver side is the **happy** failure
mode: the iterator simply blocks at the tail until the receiver
reconnects (with backoff) and starts producing again. No replayer
restart needed.

## What this loop does NOT do

- **Hot-standby reads**. The standby still doesn't accept SQL on
  client connections; the wire-level path for "I'm a standby,
  here's a snapshot you can read against" is a future loop.
  See M0005's "Promotion" and "Observability" sections.
- **Apply-LSN feedback to the primary**. The walreceiver's
  standby-status updates currently report the receiver's
  `applyLSN` (which equals its append progress, not the
  replayer's apply progress). A future loop wires
  `StreamReplayer.ApplyLSN` into the status update so the
  primary's slot tracks actual apply progress.
- **Apply throttling / parallel apply**. v0 applies single-threaded
  in arrival order, mirroring upstream's pre-parallel-apply
  default.

## Tests

- `internal/wal/stream_replayer_test.go`:
  - `TestStreamReplayerAppliesIncomingRecords` — happy path: three
    heap-inserts pushed via the writer, replayer applies them, the
    page state observed matches direct ReplayRecords.
  - `TestStreamReplayerIdempotentOnRestart` — pins the resume
    contract: re-applying an already-applied stream is a silent
    no-op via `pd_lsn`, and `ApplyLSN` still tracks `EndLSN`.
  - `TestStreamReplayerRunReturnsOnContextCancel` — pins the
    cooperative shutdown semantics.
- Existing `internal/server/walreceiver_test.go` continues to pin
  the receive path; the replayer + receiver compose mechanically
  via the writer's flush-event subscription.

## File map

- `internal/wal/recovery.go` — adds `ApplyRecord`; `ReplayRecords`
  refactored to delegate to it.
- `internal/wal/stream_replayer.go` (new) — `StreamReplayer`
  driver.
- `internal/wal/stream_replayer_test.go` (new) — three tests
  pinning the apply path, resume contract, and shutdown.
- `cmd/goopg/main.go` — `startStandbyReplayer` helper; wired
  alongside the existing `startWalreceiver` in the standby boot
  path.
