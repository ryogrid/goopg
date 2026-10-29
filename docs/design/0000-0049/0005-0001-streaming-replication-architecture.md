# Streaming Replication Architecture (M0005)

**Status**: accepted
**Milestone**: 0005 (Streaming Replication Support)
**Companion docs (planned)**:
  - `0005-0002-standby-recovery-and-replay.md`
  - `0005-0003-replication-observability.md`

## Goal

Add primary/standby physical streaming replication where WAL
records generated on the primary stream continuously to one or more
standbys and are replayed on each. Correctness and operability
first; performance tuning later. Match upstream PostgreSQL 18.3's
wire-level surface closely enough that an existing standby driver
(`pg_basebackup`, `walreceiver`) could in principle interoperate,
even if v0 only ships a goopg-on-goopg path.

## Process model

```
+-----------------+                     +-------------------+
|     PRIMARY     |   wire connection   |       STANDBY     |
|                 | ------------------> |                   |
|  walsender(s)   |     CopyBoth        |   walreceiver     |
|     ^           |                     |      |            |
|     | reads     |                     |      | applies    |
|  WAL writer     |                     |      v            |
|     ^           |                     |   recovery loop   |
|     | append    |                     |      |            |
|  user backends  |                     |      v            |
|                 |                     |   buffer pool /   |
|                 |                     |   on-disk relations|
+-----------------+                     +-------------------+
```

### Primary side

- **walsender**: per-standby goroutine on the primary. Spun up
  when a client issues a `START_REPLICATION` simple-query
  command. Reads WAL records from the local WAL writer's stream
  and forwards them to the standby via `CopyBoth`.
- **WAL writer**: unchanged from M0002
  (`internal/wal/writer.go`). Already exposes
  `WrittenLSN()` (atomic, lock-free) for poll-based wake-up.
  Walsender uses that to decide when there's new WAL to send.
- **Replication slots**: a small struct on the primary that
  tracks `(slot_name, restart_lsn, confirmed_flush_lsn)` per
  active receiver. Slots prevent WAL removal — see "Retention"
  below.
- **Connection model**: a walsender connection is just a
  regular goopg backend that, after the startup handshake,
  receives a `MsgQuery` whose payload starts with
  `START_REPLICATION`. The dispatcher recognises this and
  hands the connection over to the walsender goroutine
  instead of routing to `dispatchSimpleQueryViaExecutor`.

### Standby side

- **walreceiver**: a separate process model — standby goopg
  starts up in standby mode, opens a libpq-style connection
  to the primary, sends `START_REPLICATION SLOT slot_name
  PHYSICAL N/N`, and reads incoming `CopyData` frames from
  that single long-lived connection.
- **Recovery loop**: the standby applies received WAL records
  by reusing `internal/wal/recovery.go`'s `ReplayRecords`
  kernel — extended for incremental, one-at-a-time apply
  rather than today's bulk-on-startup mode. Detail in
  `0005-0002-standby-recovery-and-replay.md`.
- **Standby mode flag**: presence of a
  `<DataDir>/standby.signal` file at startup tells `goopg
  start` to enter standby mode (mirrors upstream).
  Configuration (`primary_conninfo`, `primary_slot_name`)
  comes from `postgresql.conf` through the existing GUC
  registry.

## Wire protocol

We follow the upstream "v3 protocol replication subset" closely
enough that a future upstream-compatible client could connect:

### Startup

A walsender connection is a normal startup with `replication=true`
in the StartupMessage parameter list. The server parses this in
the existing parameter-bag code path and stores a per-connection
`Replication=true` flag. After handshake the server emits the
usual `AuthenticationOk / S×N / K / Z` reply.

### Replication command

The first `MsgQuery` from a replication-mode connection must be
one of (v0 implements only the bolded subset):

- `IDENTIFY_SYSTEM` → returns `(systemid, timeline, xlogpos,
  dbname)` as a single-row tuple. **Required for v0.**
- `READ_REPLICATION_SLOT slot_name` → returns slot metadata.
- `CREATE_REPLICATION_SLOT slot_name PHYSICAL` → creates a slot.
  **Required for v0.**
- `DROP_REPLICATION_SLOT slot_name` → removes slot.
- **`START_REPLICATION [SLOT slot_name] PHYSICAL <lsn>
  [TIMELINE n]`** → flips connection to streaming mode.
  **Required for v0.**

`BASE_BACKUP` is **deferred**: v0 ships base-snapshot via an
out-of-band `pg_basebackup`-equivalent CLI command rather than
inlining it in the protocol, so the standby setup story is one
clear command. See "Out of scope" below.

### Streaming mode

Once `START_REPLICATION` is accepted, the walsender replies with
`MsgCopyBoth ('W')` and from then on the connection is a
bidirectional CopyBoth channel. Two PG-compatible inner message
types travel inside `CopyData` frames:

- **WAL data ('w')**: header + raw WAL bytes.
  Header (25 bytes): `'w' | startLSN(8) | endLSN(8) | sendTime(8)`
  followed by the record payload.
- **Keepalive ('k')**: `'k' | walEnd(8) | sendTime(8) |
  replyRequested(1)`. Sent every `wal_sender_timeout/2` when
  there's no new WAL, so the receiver can advance its
  apply-progress reporting without waiting on data.

The standby replies with `MsgCopyData` carrying:

- **Standby status update ('r')**: `'r' | writeLSN(8) |
  flushLSN(8) | applyLSN(8) | sendTime(8) | replyRequested(1)`.
  Sent at least every `wal_receiver_status_interval`.
- **Hot-standby feedback ('h')**: deferred — not needed for v0
  since we don't yet do snapshot-aware vacuum coordination.

This is wire-compatible with upstream's
`src/backend/replication/walsender.c` and
`src/backend/replication/walreceiver.c` framing.

### New protocol frame types to add

`internal/protocol/protocol.go` already has `MsgCopyData ('d')`
and `MsgCopyDone ('c')`. Add `MsgCopyBoth ('W')` as a backend
type. The keepalive / WAL-data / status-update inner framings
live inside `CopyData` payloads — no new outer frame types
beyond `CopyBoth`.

## State transitions

```
                            connect
   START_REPLICATION                accepts
   (catalog says
    slot exists)
        |                                ^
        v                                |
    +---------------+   slot OK   +-----------+
    |  Authenticated| ----------> | Streaming |
    +---------------+             +-----------+
                                       |
                       primary down /  | network drop
                       slot dropped    v
                                  +---------------+
                                  |  Disconnected |
                                  +---------------+
                                       |
                       reconnect       | every retry_interval
                       (configurable)  v
                                  +-----------+
                                  | Streaming |  (resume from
                                  +-----------+   last apply LSN)
```

### Standby restart invariant

A physical standby owns neither a WAL timeline nor a writable WAL tail before
promotion. Its checkpointer therefore flushes replayed data pages and CLOG on
shutdown without appending a local checkpoint record or rewriting
`pg_control`. The next walreceiver connection resumes from the durable tail of
the primary byte stream. Appending a local shutdown checkpoint would advance
that tail past the received stream and make the next `START_REPLICATION`
position point into unrelated primary WAL.

`Checkpointer.SetRecoveryMode` selects this flush-only path during
`initdb.Open` when `standby.signal` is present. This applies to periodic, SQL,
and shutdown checkpoint requests alike; promotion clears the recovery role
before any primary checkpoint can write WAL. The end-to-end standby-attach
restart test verifies that a primary insert after standby Stop plus Start is
received and visible without a forced checkpoint.

### What this is NOT: a restartpoint \(2026\-09\-22\)

The paragraph above says the standby flushes "without appending a local
checkpoint record or rewriting `pg_control`". The first half is PostgreSQL's
rule; **the second half is a goopg gap, not a design choice**, and it is
recorded here so the next reader does not take it for parity.

PostgreSQL's standby counterpart to a checkpoint is `CreateRestartPoint`
\(`postgres/src/backend/access/transam/xlog.c`\). It calls `CheckPointGuts` —
the same durability half goopg's `runRecoveryFlush` performs — and then
**advances `pg_control`** from the LAST REPLAYED checkpoint record:
`ControlFile->checkPoint`, `ControlFile->checkPointCopy`, and, in
`DB_IN_ARCHIVE_RECOVERY`, `minRecoveryPoint`/`minRecoveryPointTLI` up to that
record's end. It never writes a NEW checkpoint record — it reuses one the
primary already sent, which is exactly why it is safe.

goopg does the flush and skips the `pg_control` advance. The consequence is
bounded and not a correctness bug: a restarted standby replays from an older
position than PostgreSQL would, and its `pg_control` reports a stale checkpoint
to any reader inspecting the directory \(including PG's own tools\). The resume
point is in the deferral ledger, 2026\-09\-22, M0122\-0013.

### The restartpoint \(2026\-10\-27, M0122\-0013\)

The gap above is closed.

- **Replay remembers the checkpoint.** `StreamReplayer` records the last
  checkpoint record it applies as a `ReplayedCheckpoint`: the record's start and
  end plus its decoded `CheckPoint` struct. This is PG's `RecoveryRestartPoint`
  filling `XLogCtl->lastCheckPoint`, `lastCheckPointRecPtr` and
  `lastCheckPointEndPtr`.
- **The checkpointer uses it.** Standby startup installs that source
  \(`Checkpointer.SetRestartPointSource`\), and promotion clears it.
- **`runRecoveryFlush` follows `CreateRestartPoint`.** It takes its copy of the
  replayed checkpoint BEFORE the flush \(`CheckPointGuts`\), so every page the
  checkpoint's redo depends on is covered. After the flush, `adoptRestartPoint`
  updates `pg_control`, but only when `checkPointCopy.redo` is older than the
  replayed checkpoint's redo:
  - `checkPoint` becomes the record's 0\-based start;
  - `checkPointCopy` becomes the whole replayed struct;
  - `minRecoveryPoint` rises to the record's end only in
    `DB_IN_ARCHIVE_RECOVERY`, as upstream.

Still no WAL is written.

Startup crash recovery begins at `checkPointCopy.redo` \(`beginRecovery` →
`ReplayFromDirWithMgrAt`\), so a restarted standby now replays from its last
restartpoint instead of from its base backup's checkpoint.

`TestE2E_PhysicalStandbyRestartPointAdvancesPgControl` covers the whole path:

- the primary checkpoints and the standby replays that checkpoint;
- a standby `CHECKPOINT` adopts it, and the standby's `pg_control` then equals
  the primary's checkpoint and redo;
- after a restart the standby keeps the replayed row and keeps streaming.

With the wiring removed, the test fails on an unchanged redo.

**Still missing** \(ledgered 2026\-10\-27\):

- Restartpoints do not drive WAL segment removal \(`KeepLogSeg` /
  `RemoveOldXlogFiles` in `CreateRestartPoint`\).
- ~~The skip arm's `UpdateMinRecoveryPoint` is not ported.~~ Ported 2026-10-29 (see "Restartpoint skip arm raises
  minRecoveryPoint").

### A standby's pg\_control state \(2026\-10\-27, M0122\-0013\)

A goopg standby used to stamp `DB_IN_PRODUCTION` at startup, like a primary,
and `DB_IN_CRASH_RECOVERY` while replaying. That made the restartpoint's
`minRecoveryPoint` arm dormant and gave every tool reading `pg_control` the
wrong picture.

PG treats `standby.signal` as `ArchiveRecoveryRequested`
\(`xlogrecovery.c:946-961`\). goopg now follows that through the whole lifecycle:

| moment | state stamped | PG reference |
|---|---|---|
| replay begins \(`beginRecovery`\) | `DB_IN_ARCHIVE_RECOVERY`, not `DB_IN_CRASH_RECOVERY` | `xlogrecovery.c:946-961` |
| end of Open | `DB_IN_ARCHIVE_RECOVERY` \(`stampInArchiveRecovery`\) | the standby serves in archive recovery |
| shutdown restartpoint | `DB_SHUTDOWNED_IN_RECOVERY` | `CreateRestartPoint` |
| promotion | `DB_IN_PRODUCTION` | end of `StartupXLOG`, `xlog.c:6204-6211` |

The shutdown restartpoint stamps `DB_SHUTDOWNED_IN_RECOVERY` in two cases:

- when it adopts a newer checkpoint while in archive recovery;
- unconditionally when there is nothing newer to adopt, as the skip arm does
  \(`xlog.c:7685-7692`\).

A restarted standby replays from either state, since neither is
`DB_SHUTDOWNED`.

`TestE2E\_PhysicalStandbyRestartPointAdvancesPgControl` now asserts each state
in turn: running, stopped, restarted, promoted.

The complementary primary-restart boundary is pinned separately: a running
standby must survive a primary Stop plus Start, redial the same endpoint, and
replay a row committed after the primary returns. The reconnect loop retains
the received WAL tail across the disconnected interval, so this path exercises
the ordinary walreceiver retry rather than standby bootstrapping.

A crash-time disconnect has an additional catch-up obligation: the primary
can commit while no receiver is connected, while the physical slot preserves
the standby's restart position. On standby recovery, the receiver resumes from
its durable tail and replays the complete outage interval in order.
`TestE2E_PhysicalReplicationCatchesUpAfterStandbyCrash` kills a caught-up
standby, commits twelve rows on the live primary, then asserts every row after
the standby recovers and reconnects.

### Restartpoint WAL removal \(2026\-10\-28, M0122\-0013\)

After `CreateRestartPoint` updates pg\_control it deletes the WAL that the restartpoint made redundant (xlog.c):

1. `XLByteToSeg(RedoRecPtr)` takes the restartpoint's redo segment.
2. `KeepLogSeg` lowers it for replication slots and `wal_keep_size`, measured against the later of the received and
   replayed positions.
3. `RemoveOldXlogFiles` removes or recycles every older segment.

Only the arm that adopted a restartpoint gets there; the skip arm returns before it. Until now a goopg standby kept
every segment it ever received.

`runRecoveryFlush` now calls the checkpointer's retainer with the adopted redo, right after `adoptRestartPoint`
reports an advance. That is the same `SlotAwareRetainer` the primary's checkpoint uses, with the same slot horizons,
`max_slot_wal_keep_size` invalidation and recycle-by-rename sizing. On a standby the retainer's writer is the one the
walreceiver appends into, so its `WrittenLSN`, the retainer's lag yardstick, is the end of the received WAL: PG's
`endptr`. Replay has already passed the adopted checkpoint record, which follows its redo, so no segment the
replayer or a restarted walreceiver still needs is removed.

`TestE2E_PhysicalStandbyRestartPointRemovesOldWAL` writes about 40 MB on the primary, checkpoints, and waits for the
standby to replay past the checkpoint. It then requires:

- before the standby CHECKPOINT, segments older than the redo's segment exist;
- after it, none remain;
- the standby still restarts and keeps streaming.

With the retention call removed it fails: two old segments survive.

### Restartpoint distance estimate \(2026\-10\-28, M0122\-0013\)

`CreateRestartPoint` also feeds the checkpoint distance estimate before removing old WAL (xlog.c). It takes
`PriorRedoPtr` as pg\_control's `checkPointCopy.redo` before the update, and runs
`UpdateCheckPointDistanceEstimate(RedoRecPtr - PriorRedoPtr)` only in the arm that adopted a restartpoint.

goopg now does the same:

- `adoptRestartPointPrior` reports the prior redo it read.
- `runRecoveryFlush` calls `noteRestartPointDistance(priorRedo, redo)` right before the retainer.
- The retainer's recycled-spare sizing (`computeSpareSegments`, PG's XLOGfileslop) therefore follows the standby's
  own restartpoint cadence. Before this the estimate stayed 0 on a standby.
- Because the prior redo comes from pg\_control, the first restartpoint after a start already measures a distance.
- The adopted redo becomes the checkpointer's prior redo, so the first checkpoint after a promotion measures from
  it, as PG's would from pg\_control.

Pin: `TestRecoveryFlushAdoptsReplayedCheckpoint` asserts an estimate of 8192 − 16 after the restartpoint, and no
change from a source-less flush.

### Restartpoint moves RedoRecPtr \(2026\-10\-28, M0122\-0013\)

`CreateRestartPoint` sets `RedoRecPtr = XLogCtl->RedoRecPtr = lastCheckPoint.redo` and makes pg\_control's
`checkPoint` the restartpoint's record (xlog.c). `RedoRecPtr` is what `XLogCheckpointNeeded` measures elapsed
segments from. Replay calls that when it switches segments (xlogrecovery.c XLogPageRead) and requests the next
restartpoint once `CheckPointSegments − 1` segments have gone by.

goopg's restartpoint never moved the checkpointer's redo anchor (`lastCheckpointRedoLSN`). Every runtime's
checkpointer, a standby's included, gets `max_wal_size` (cmd/goopg/main.go), and a standby runs in recovery mode,
so `runCheckpoint` routes to `runRecoveryFlush`. The volume trigger kept the anchor it seeded at start, and once
`max_wal_size / (1 + checkpoint_completion_target)` of WAL had arrived (512 MB at the defaults), every 1-second volume
tick ran a full restartpoint flush and data-file sync.

`runRecoveryFlush`'s adopted arm now stores:

- the restartpoint's redo as `lastCheckpointRedoLSN`, which re-anchors `volumeTriggerFires`;
- its record start and end as `lastCheckpointStartLSN` / `lastCheckpointLSN`.

`CheckpointRedoLSN` / `LastCheckpointRecordLSN` therefore answer a BASE\_BACKUP from the standby with the last
restartpoint, as PG's backup-from-standby starts there, instead of 0 and the received-WAL fallback. After a
promotion, the first checkpoint's FPI epoch measures from the last restartpoint's redo, as PG's would.

Pin: `TestRecoveryFlushAdoptsReplayedCheckpoint` asserts the redo (8193, 1-based) and the record start after the
restartpoint. Removing the stores fails it.

### Restartpoint skip arm without the flush \(2026\-10\-28, M0122\-0013\)

When there is no replayed checkpoint, or the last one's redo is not newer than pg\_control's `checkPointCopy.redo`,
`CreateRestartPoint` returns before `CheckPointGuts` (xlog.c). It updates `minRecoveryPoint`, and for a shutdown
request stamps `DB_SHUTDOWNED_IN_RECOVERY`, but flushes no buffer, CLOG page or data file.

goopg flushed everything first and only then found nothing to adopt, so every timed request on a standby that had
seen no new checkpoint record was a full flush and data-file sync. `runRecoveryFlush` now calls
`restartPointSkips` (same test, against pg\_control) before the flush, and in the skip arm only stamps the shutdown
state. A restarted standby replays from pg\_control's redo, so pages left unflushed are recovered from WAL, as in PG.
Without a data directory or pg\_control the flush runs as before.

Pin: `TestRecoveryFlushAdoptsReplayedCheckpoint` counts flushes. The adopting restartpoint flushes once; a repeat
with nothing newer, and a source-less one, flush zero times. Forcing the skip test off fails it. The standby restart,
promotion and base-backup E2Es pass.

Still missing (ledgered):

- ~~the standby's volume trigger measures the received WAL (`WrittenLSN`), where PG measures the segment replay is
  reading~~ — resolved by the next section;
- no `PreallocXlogFiles`;
- no `TruncateSUBTRANS` at a restartpoint;
- ~~no `archive_cleanup_command`~~ — resolved 2026-10-29 (see "archive\_cleanup\_command at a restartpoint");
- restartpoints still follow the checkpointer's timer instead of replay's `CheckPointSegments`-driven
  `RequestCheckpoint`.

### Restartpoint skip arm raises minRecoveryPoint \(2026\-10\-29, M0122\-0013\)

The skip arm returns before `CheckPointGuts`, but first runs `UpdateMinRecoveryPoint(InvalidXLogRecPtr, true)`
(xlog.c). Called that way, in the checkpointer, the function behaves as follows:
- If pg\_control's `minRecoveryPoint` is unset, nothing changes (`updateMinRecoveryPoint` goes false).
- Otherwise `minRecoveryPoint` rises to `GetCurrentReplayRecPtr()`, the end of the last replayed record, whenever that
  is later. This keeps a standby restarted afterwards from opening for read-only connections at an earlier point.

goopg's skip arm only stamped the shutdown state. `runRecoveryFlush` now calls `raiseMinRecoveryPointToReplay` with
the checkpointer's replay-position source. That source is `StreamReplayer.ApplyLSN`, which already equals PG's
`lastReplayedEndRecPtr`, so no unit conversion is needed. The timeline stays pg\_control's
`minRecoveryPointTLI`, falling back to `checkPointCopy`'s when unset. goopg's replayer follows one timeline and
records none; that gap is ledgered.

Pin: `TestRestartPointSkipRaisesMinRecoveryPointToReplay` covers three cases: raised, replay behind, and unset stays
unset. It also asserts the skip arm still flushes nothing. Neutralising the call fails it.

### archive\_cleanup\_command at a restartpoint \(2026\-10\-29, M0122\-0013\)

`CreateRestartPoint` ends by running `archive_cleanup_command` through `ExecuteRecoveryCommand` (xlog.c,
xlogarchive.c):
- `%r` becomes the WAL file name of the oldest restartpoint. `GetOldestRestartPoint` reads pg\_control's
  `checkPointCopy.redo` and timeline, which the restartpoint has just updated.
- Placeholder substitution follows `replace_percent_placeholders`: `%%` is a literal `%`, and an unknown letter or a
  trailing `%` is an invalid parameter value.
- The command runs through `system()`. A non-zero exit is a WARNING (`failOnSignal` false).

Until now goopg did not register the GUC at all. It existed only in the generated list of PG parameter names, so
neither `SHOW` nor the restartpoint knew it. Now:
- the GUC is registered as PG defines it (string, `''`, PGC\_SIGHUP), with its `postgresql.conf.sample` line;
- `xlog.ReplacePercentPlaceholders` and `xlog.ExecuteRecoveryCommand` port the two C helpers;
- the adopted restartpoint arm calls `runArchiveCleanupCommand` after WAL retention, and the skip arm returns before
  it;
- the checkpointer reads the GUC through `SetArchiveCleanupCommandSource` at every restartpoint, so a reloaded value
  takes effect.

Pins: `TestReplacePercentPlaceholders`; `TestRestartPointRunsArchiveCleanupCommand`, which checks that `%r` is segment
3 for a redo in segment 3 and that the skip arm runs nothing.

Still missing (ledgered): ~~`recovery_end_command` is unregistered~~ — resolved 2026-10-29 (next section);
`restore_command` is registered as postmaster\-context where PG 18 has PGC\_SIGHUP; the command's
`ArchiveCleanupCommand` wait event is not reported.

### recovery\_end\_command at the end of archive recovery \(2026\-10\-29, M0122\-0013\)

PG runs `recovery_end_command` once archive recovery ends. `StartupXLOG` calls `CleanupAfterArchiveRecovery` (xlog.c)
when `ArchiveRecoveryRequested`, after `PerformRecoveryXLogAction` and `XLogReportParameters` and before it stamps
`DB_IN_PRODUCTION`. The command goes through `ExecuteRecoveryCommand` with `failOnSignal` true:
- `%r` is the WAL file holding pg\_control's checkpoint-copy redo (`GetOldestRestartPoint`).
- A command whose wait status `wait_result_is_any_signal(rc, true)` reports as signalled is FATAL. That covers a
  signal-killed child and a shell exit above 125 (src/common/wait_error.c).
- Any other failure is a WARNING.

goopg ends archive recovery in two places. Both now run the command through `runRecoveryEndCommand`
(cmd/goopg/standby.go):
- **Standby promotion** (`standbyController` promote path): after the checkpointer leaves recovery mode, before the
  `DB_IN_PRODUCTION` stamp. A FATAL fails the promotion. That promotion stays retryable, because the in-flight guard is
  released on every exit.
- **recovery.signal promotion** (`promoteAfterRecovery`): after the timeline switch, before `recovery.signal` is
  removed. A FATAL aborts the start and leaves the signal file, so the next start retries recovery. That is the
  restart PG's FATAL in the startup process implies.

Other changes:
- The GUC is registered as PG defines it (string, `''`, PGC\_SIGHUP), with its `postgresql.conf.sample` line.
- `xlog.ExecuteRecoveryCommand` now also returns the any-signal classification (`waitResultIsAnySignal`).
  `archive_cleanup_command` keeps WARNING for every failure (`failOnSignal` false).

Pins:
- `TestExecuteRecoveryCommandSignalClassification`: `exit 3` is a plain failure; `exit 127` and `kill -9 $$` count as
  signals.
- `TestStandbyControllerPromoteRunsRecoveryEndCommand`: checks `%r`, that a plain failure promotes, and that a signal
  fails the promotion and leaves pg\_control out of production.
- `TestPromoteAfterRecoveryRunsRecoveryEndCommand`: on success the signal file is removed; on a signal it is kept.

A literal `%` in the command must be written `%%`, as in PG. The first version of the test tripped over this: its
temp-dir name contained `%r`.

Still missing (ledgered): no `RecoveryEndCommand` wait event; `RemoveNonParentXlogFiles` and the `.partial`
renaming of the last old-timeline segment, which `CleanupAfterArchiveRecovery` performs after the command, are not
ported.

### Restartpoint requests follow the replayed WAL \(2026\-10\-28, M0122\-0013\)

On a standby PG requests a restartpoint from the startup process's read loop: `XLogPageRead` (xlogrecovery.c), when
it leaves a segment, calls `XLogCheckpointNeeded(readSegNo)` and, if it holds, `RequestCheckpoint(CHECKPOINT_CAUSE_XLOG)`.
The distance is the same `CheckPointSegments - 1` the primary uses, measured from `RedoRecPtr` (the last restartpoint's
redo); the position is the segment **replay** is reading, not the end of the received WAL.

goopg's checkpointer polls the max\_wal\_size trigger once a second against the writer's `WrittenLSN`, which on a
standby is the received WAL. A startup process that lagged the receiver (or a `recovery_min_apply_delay`) therefore
requested restartpoints for WAL it had not replayed, and, because a restartpoint with no newer replayed checkpoint
leaves the anchor in place, it requested one on every poll; each was a pg\_control read in the skip arm.

Now:

- `Checkpointer.SetReplayPositionSource` installs the replayer's `ApplyLSN` (standby startup, `cmd/goopg/standby.go`;
  cleared at promotion). In recovery mode `volumeTriggerFires` measures that position.
- In recovery a request fires once per replay segment (`recoveryVolumeSegNo`): PG evaluates the test only when replay
  leaves a segment, so the next request comes from the next segment, not the next poll.
- The primary path is unchanged: outside recovery the source is ignored, and without a source a standby falls back to
  the writer's position.

Remaining difference: PG tests the segment just finished (`readSegNo` before the switch), goopg the segment holding
the last applied record — the same count once replay is inside the next segment, at most one poll apart.

Pin: `TestCheckpointerVolumeTriggerRecoveryMeasuresReplay` — received WAL 40 segments ahead with replay in the
anchor's segment does not fire; 3 replayed segments do; the same segment does not re-request; the next does; an
adopted redo re-anchors; the primary ignores the source. Measuring the writer, or dropping the once-per-segment
guard, fails it.

### Promotion restart invariant

A caught-up standby promoted through `goopg promote -D <dir>` must become a
durable primary, not merely a writable process until its next restart. The
promotion controller persists its new timeline in both the timeline file and
`pg_control`, removes `standby.signal`, and returns its checkpointer to normal
WAL-producing mode. A subsequent stop plus start must therefore retain the
replayed history and accept a new primary write.

`TestE2E_PhysicalPromotionPersistsAcrossRestart` pins this sequence: it stops
the old primary after the standby catches up, promotes the standby, writes a
row, restarts the promoted node, and writes another row. The final ordered
result proves both the received and post-promotion histories survived the
handoff.

The same promotion path is valid after an abrupt primary loss. A caught-up
standby need not wait for a clean walsender shutdown before ownership changes:
`TestE2E_PhysicalPromotionAfterPrimaryCrash` kills the old primary, promotes
the standby, and verifies both its replayed row and its first locally-written
row on the new primary.

## Hooks into existing goopg code

This section is the implementation seam list — what each
follow-up loop will touch.

### Wire layer

- `internal/protocol/protocol.go:44-82` — add `MsgCopyBoth`
  byte = `'W'` next to existing `MsgCopyOutResponse`.
- `internal/protocol/messages.go` — add encoders for the
  WAL-data, keepalive, and standby-status payloads.
- `internal/server/server.go:319-396` (`serveConn`) — recognise
  `replication=true` in startup parameters; tag the per-conn
  ctx with `IsReplication`.
- `internal/server/server.go:552-650` (`runPostStartupLoop`) —
  intercept `MsgQuery` for replication commands when
  `IsReplication`; route to a new `handleReplicationCommand`
  in `internal/server/replication.go` (new file).

### Control layer

- `internal/control/control.go:234-277` — add `PROMOTE` command;
  new `OnPromote` callback in `startControlPlane`
  (`server.go:250-266`).
- `cmd/goopg/main.go` — add `goopg promote -D <dir>`
  subcommand that sends `PROMOTE` over the control socket.

### WAL layer

- `internal/wal/reader.go:19-68` — gain a streaming
  `RecordIterator` that yields one record at a time starting
  from a given LSN, blocking when caught up to
  `WrittenLSN()`. Today only `ReadAll` exists.
- `internal/wal/writer.go:71` — add a small subscription
  channel callers can register on so walsender goroutines
  wake on WAL-flush events instead of polling
  `WrittenLSN()`.
- `internal/wal/recovery.go:338-386` (`ReplayRecords`) — split
  the kernel so single-record replay can be invoked outside
  the bulk startup path. Already idempotent via `pd_lsn`
  comparison (lines 474, 509, 541), so resumption from any
  LSN is safe.

### Catalog / state

- `internal/wal/slots.go` (new) — `Slot{Name, RestartLSN,
  ConfirmedFlushLSN, Active}`. Persisted to
  `<DataDir>/pg_replslot/<slot_name>/state` (mirrors upstream
  `slotdata.c`). Loaded at startup; rewritten on
  `CONFIRMED_FLUSH_LSN` updates from standby status replies.
- The WAL writer's segment-removal path (M0002 retention)
  must consult `min(slot.RestartLSN)` before unlinking, with
  `max_slot_wal_keep_size` as the upper bound on retention.

## Replication slot retention semantics

The primary's WAL recycling/retention path (today triggered by
the M0002 `max_wal_size` checkpoint volume rule) needs to honour
slot reservations:

```
oldest_required_lsn = min(checkpoint_lsn,
                          min(slot.RestartLSN  ∀ active slots))
```

Plus the upstream-aligned safety knob `max_slot_wal_keep_size`:
if a slot's lag exceeds this, the slot is **invalidated**
(marked as such, restart_lsn cleared) and WAL recycling
proceeds. The standby reconnecting against an invalidated slot
gets an error and must rebuild from a fresh base backup. v0's
default for this knob mirrors upstream's `-1` (unlimited).

## Configuration surface

GUCs the M0005 implementation will register (consumed in later
loops):

Primary side:
- `wal_level` (`replica` | `minimal`) — must be ≥ `replica` for
  any walsender to start. Default flips to `replica` in v0.
- `max_wal_senders` (default 10).
- `max_replication_slots` (default 10).
- `wal_sender_timeout` (default 60s).
- `max_slot_wal_keep_size` (default -1 / unlimited).

Standby side:
- `primary_conninfo` (libpq DSN).
- `primary_slot_name`.
- `wal_receiver_status_interval` (default 10s).
- `recovery_target_timeline` (default `latest`; v0 supports
  only single-timeline operation, so the value is logged but
  not yet enforced).
- `hot_standby` (default `on` in v0; standby accepts read-only
  queries during replay).

### `primary_conninfo` key parsing (2026-07-08)

`cmd/goopg/main.go`'s `parsePrimaryConninfoFull` parses `host`,
`port`, `application_name`, `user`, and (as of this loop) `sslmode`
out of the libpq-style DSN. `sslmode` is enforced, not just captured:
goopg has no TLS implementation, so `internal/server`'s
`DialWalReceiver` (via `checkSSLMode`) accepts `disable`/`allow`/
`prefer`/unset (all connect in plaintext — the same outcome `prefer`
would negotiate down to against any server that doesn't speak SSL)
and rejects `require`/`verify-ca`/`verify-full` before dialing, rather
than silently connecting in plaintext when the operator asked for
encryption. `password` remains unparsed: the replication handshake
(`WalReceiver.handshake`) never reads an `Authentication*` challenge
from the primary (v0 is trust-only per `WalReceiverConfig.User`'s doc
comment), so there is nowhere for a password to be consumed yet —
tracked as a deferred item pending real replication auth, not silently
dropped.

## Test strategy

The M0005 acceptance test (see milestone DoD #4) is an
end-to-end goopg-on-goopg run that:

1. starts a primary cluster,
2. runs a base-backup CLI to seed a standby data directory,
3. starts the standby pointed at the primary's listen address +
   slot name,
4. issues writes to the primary and observes them on the
   standby (`SELECT count(*) FROM t` matches after the standby
   has caught up),
5. kills the primary, promotes the standby, and confirms the
   promoted node accepts new writes.

The harness lives at `internal/testutil/replcluster/` (mirroring
the existing `internal/testutil/cluster/`). A new design doc
`0005-0003-replication-observability.md` will define the
status-view surface the test queries.

### M0122 lifecycle closure (2026-09-22)

The single-timeline goopg-to-goopg lifecycle boundary is covered by direct
integration tests: ordinary streamed visibility, a standby restart, a primary
restart with walreceiver reconnect, standby-outage catch-up from its retained
slot, and promotion following both orderly shutdown and abrupt primary loss.
Promotion also has a cold-restart durability test for the new primary. These
tests intentionally do not claim an in-process WAL timeline switch: `Writer`'s
timeline is immutable for its process lifetime, and the M0130-S8 owner covers
that multi-timeline protocol work.

## Out of scope

- `BASE_BACKUP` over the wire protocol. v0 ships base-snapshot
  via a CLI tool that copies data files + reads pg_control;
  full upstream-compatible BASE_BACKUP is a follow-up.
- Logical replication (deferred per milestone definition).
- Synchronous replication / quorum commit (deferred per
  milestone definition).
- Multi-timeline operation. v0 supports a single timeline;
  recovery_target_timeline is accepted but only `latest` is
  honoured.
- Cascading replication (standby-of-standby). Single-hop only.
- Authentication for replication connections beyond what
  regular SQL connections already get (trust / md5 /
  scram-sha-256 from `internal/auth/`).

## References

- Upstream PostgreSQL 18.3:
  `postgres/src/backend/replication/walsender.c`,
  `postgres/src/backend/replication/walreceiver.c`,
  `postgres/src/backend/replication/slot.c`.
- Wire protocol: `postgres/src/include/replication/walprotocol.h`.
- Existing M0002 design docs that this work composes with:
  - `0002-0001-checkpointing.md` (WAL writer + retention
    machinery this layers on top of).
  - `0002-0003-redo-records.md` (logical record kinds the
    standby will replay).
- This milestone: `docs/milestones/0005-streaming-replication-support.md`.
