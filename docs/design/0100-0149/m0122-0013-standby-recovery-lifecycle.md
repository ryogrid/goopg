# M0122-0013 — standby recovery lifecycle (restartpoints, recovery commands, admission, shutdown)

Split out of [0005-0001 — Streaming Replication Architecture](../0000-0049/0005-0001-streaming-replication-architecture.md)
on 2026-10-30 under design-doc rule D3 (that document passed 800 lines). The sections below are the M0122-0013 work on
a standby's recovery lifecycle, moved verbatim and in their original order; new M0122-0013 standby sections are added
here. Archive recovery through recovery.signal (restore\_command, timelines, targets, promotion) is documented in
[0130-0009](0130-0009-recovery-signal-archive-recovery.md).

## Sections

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
- ~~no `TruncateSUBTRANS` at a restartpoint~~ — resolved 2026-10-29 (see "pg\_subtrans truncation at a restartpoint");
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

### Recovery parameters are validated at start; restore\_command is PGC\_SIGHUP \(2026\-10\-29, M0122\-0013\)

When archive recovery was requested, PG's `validateRecoveryParameters` (xlogrecovery.c) checks the compulsory
parameters:
- **Standby mode:** having neither `primary_conninfo` nor `restore_command` is a WARNING ("specified neither …";
  hint: the server polls `pg_wal`).
- **Archive recovery without standby mode:** a missing `restore_command` is FATAL ("must specify
  \"restore\_command\" when standby mode is not enabled").

goopg handled the second case by logging a warning, **deleting `recovery.signal`, and starting as a primary**. That
opens a restored base backup without replaying its archived WAL, which is the inconsistent state the FATAL exists to
prevent.

What changed:
- `validateRecoveryParameters` (cmd/goopg/standby.go) now ports both arms. `goopg start` exits 1 with PG's message, and
  `recovery.signal` stays so the operator can supply the command and restart.
- `restore_command` is registered as PGC\_SIGHUP, as in PG 18 (guc\_tables.c), with PG's sample-file lines. Its one
  consumer, the start-time archive recovery, reads it before the server accepts connections.

Pins:
- `TestValidateRecoveryParameters` covers six signal/parameter combinations, and checks PG's error text and warning.
- `TestStartRecoverySignalWithoutRestoreCommandIsFatal` runs `goopg start` and checks exit 1, the FATAL text, and that
  `recovery.signal` is kept.

Still missing (ledgered): goopg's archive recovery reads `restore_command` once, at start. PG's startup process
re-reads it after a SIGHUP while recovery runs, which matters for a long `recovery.signal` recovery. The rest of
`validateRecoveryParameters` (recovery\_target\_action, recovery\_target\_time parsing, recovery\_target\_timeline)
is not ported here.

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


### pg\_subtrans truncation at a restartpoint \(2026\-10\-29, M0122\-0013, `8aa917e47`\)

`CreateRestartPoint` (xlog.c) runs `TruncateSUBTRANS(GetOldestTransactionIdConsideredRunning())` after WAL retention
and before `archive_cleanup_command`. goopg's `TruncateSubtransFn` hook ran only after a primary checkpoint, so a
long-running standby's subxact map and its pg\_subtrans segments grew without bound.

The adopted restartpoint arm of `runRecoveryFlush` now calls the hook. The skip arm returns before it, as PG's does.

PG skips the truncation when `hot_standby` is off, because `StartupSUBTRANS` has not run yet. goopg's standby always
serves reads and its subxact map is always live, so it truncates on every adopted restartpoint. `hot_standby` itself
is registered but not consumed (ledgered). The hook's horizon is at most datfrozenxid, which is older than any
transaction still running on the primary, so the truncation is safe on a standby.

Pin: `TestRestartPointTruncatesSubtrans` (adopted arm truncates once, skip arm never; mutation-checked).

### hot\_standby = off refuses connections to a standby \(2026\-10\-29, M0122\-0013, `1821d1af2`\)

`hot_standby` was registered (PGC\_POSTMASTER, default on) but goopg never read it, so a standby always accepted
connections. PG with `hot_standby = off` accepts none while it is in recovery:
- `canAcceptConnections` (postmaster.c) returns `CAC_NOTHOTSTANDBY` in `PM_RECOVERY`.
- backend\_startup.c raises FATAL 57P03 "the database system is not accepting connections", DETAIL "Hot standby mode
  is disabled.". This happens after the startup packet and before authentication.
- Replication connections are refused too.
- Cancel requests still work, because they are handled while reading the startup packet.
- A promotion (`PM_RUN`) lifts the refusal.

What changed:
- `postmaster.Config.HotStandbyDisabled` is read from `hot_standby` at start.
- `Server` refuses right after `handleStartup` whenever `IsStandby()` reports recovery. The new helper
  `writeFatalDetail` builds the error.

Pin: `TestHotStandbyDisabledRefusesConnections` (mutation-checked). It checks that normal and replication
connections are refused with PG's fields, that the same server accepts connections after promotion, and that a
standby with `hot_standby = on` accepts them.

Still different: PG with `hot_standby = on` also refuses connections until recovery reaches consistency or the
recovery snapshot is ready ("the database system is not yet accepting connections"). goopg's standby accepts reads
as soon as it starts (ledgered).

## Update 2026-10-30 — hot standby waits for a consistent recovery state (M0122-0013, `8527ab50e`)

With `hot_standby = on`, PG's postmaster stays in PM\_RECOVERY until the startup process reaches a consistent recovery
state (`CheckRecoveryConsistency` → `PMSIGNAL_BEGIN_HOT_STANDBY`). Until then `canAcceptConnections` returns
CAC\_NOTHOTSTANDBY for every connection, normal or replication: FATAL 57P03 "the database system is not yet accepting
connections", with the detail "Consistent recovery state has not been yet reached.". goopg's standby accepted reads at
once.

- `standbyController` computes the consistency point from pg\_control as `InitWalRecovery` leaves it: `max(minRecoveryPoint,
  checkpoint redo, backupEndPoint)`. A `backupEndRequired` backup never becomes consistent by position.
- `Consistent()` latches once the replayed position passes that point, and logs "consistent recovery state reached at X/X".
- `postmaster.Config.HotStandbyConsistent` drives the refusal, which sits right after the startup packet next to the
  `hot_standby = off` refusal.
- A base-backup standby is consistent as soon as replay passes the backup's redo point. The replication and promotion
  end-to-end tests are unaffected.

**Pins:** `TestHotStandbyNotConsistentRefusesConnections` and `TestStandbyConsistencyPoint`. Both were mutation-checked.

**Still open (ledgered):**
- The recovery-snapshot readiness wait (STANDBY\_SNAPSHOT\_READY from XLOG\_RUNNING\_XACTS).
- `XLOG_BACKUP_END`, so `backupEndRequired` backups never become consistent.
- `ReachedEndOfBackup`'s pg\_control update.

## Update 2026-10-30 — SIGQUIT is the immediate shutdown (M0122-0013, `108e7a630`)

goopg did not catch SIGQUIT. `pg_ctl stop -m immediate` sends it, so it hit Go's default action: a dump of every
goroutine, exit 2, and `postmaster.pid` left behind. PG's postmaster treats SIGQUIT as the immediate shutdown (pmdie): it
logs "received immediate shutdown request", quits its children, writes no checkpoint, and exits 0 with its lock files
removed.

- `goopg start` registers SIGQUIT for its whole life (`immediateShutdown`):
  - **Before the server runs**, it marks the runtime so no shutdown checkpoint is written, records the request, and
    cancels the start context. Crash recovery's caller returns at once. Archive recovery stops at its next check and
    writes nothing: pg\_control keeps "in archive recovery", `recovery.signal` stays, and the `restore_command` child
    is sent SIGTERM.
  - **Once the server runs**, it calls `Server.StopImmediate`, the path the control socket's STOPIMMEDIATE now shares.
- `goopg stop -mode immediate` on a server that has no control socket yet sends SIGQUIT, as `pg_ctl` does. It used to
  refuse.
- **Probes:**
  - A running server with a session in `pg_sleep` stopped in 0.1 s with exit 0 and pg\_control "in production". The next
    start crash-recovered the data.
  - SIGQUIT during archive recovery left "in archive recovery" and `recovery.signal`.

**Pin:** `TestE2E_GoopgSigquitIsImmediateShutdown`. With the handler unregistered it fails: no log line, a goroutine
dump, and `postmaster.pid` left behind.

**Still open (ledgered):**
- Sessions get the fast-shutdown messages instead of quickdie's "terminating connection due to immediate shutdown
  command".
- The immediate path still waits for session goroutines.
- Crash replay is not interruptible.

## Update 2026-10-30 — crash replay stops on a shutdown request (M0122-0013, `6c78ab6c4`)

`initdb.Open`'s crash replay could not be stopped, so a SIGTERM, SIGINT or SIGQUIT during crash recovery waited for the
whole replay. PG's startup process checks for interrupts before every record (`ProcessStartupProcInterrupts`) and
exits on a shutdown request.

- `xlog.ReplayFromDirWithMgrAtInterruptible` asks `interrupted` before every record. Once it reports true, replay
  returns an error wrapping `ErrRecoveryShutdown`. The old entry points pass nil.
- `initdb.OpenOptions.Interrupted` is passed through to crash replay.
- `goopg start` passes `ctx.Err() != nil`: SIGTERM and SIGINT cancel the context, and SIGQUIT does too through
  `immediateShutdown`. `endInterruptedCrashRecovery` then ends the start:
  - a fast shutdown stamps `DB_SHUTDOWNED_IN_RECOVERY` (the shutdown restartpoint's skip arm);
  - an immediate shutdown writes nothing, so `DB_IN_CRASH_RECOVERY` stays;
  - both exit 0.
- Nothing is written to WAL, because replay runs before the WAL writer opens. The next start replays from the same
  redo point.

**Pins:** `TestOpenCrashReplayIsInterruptible` (fails with the check removed) and `TestEndInterruptedCrashRecovery`.

**Still open (ledgered):** Open's post-replay phases still run to completion once replay has finished.

## Update 2026-10-30 — sessions report why a stop ends them (M0122-0013, `f97dccda6`, `2bc45c8b3`)

On a stop PG's backends say why they are going away before the socket closes. goopg either said nothing (idle
sessions) or sent the fast-shutdown cancel.

| stop | PG | goopg before | goopg now |
|---|---|---|---|
| immediate, idle or busy | quickdie (`PMQUIT_FOR_STOP`): NoticeResponse WARNING 57P01 "terminating connection due to immediate shutdown command" | nothing (idle) / ERROR 57014 "canceling statement due to user request" (busy) | the quickdie notice |
| fast, idle | die() then `ProcessInterrupts`: FATAL 57P01 "terminating connection due to administrator command" | nothing | the FATAL |
| fast, busy | the same FATAL alone — `ProcDiePending` is checked before `QueryCancelPending` | ERROR 57014, then the FATAL | the FATAL alone |

- `Server.StopImmediate` sets `immediateStopping` before it cancels Run's context.
- Idle sessions: the connCtx watcher's deadline breaks `ReadFrame`. When Run's context is cancelled, the read-error
  branch writes the message, with a fresh write deadline, before it returns.
- Busy sessions: `shutdownEndsStatement` is shared by both error writers, `writeQueryError` (simple query) and
  `writeExtendedMessageError` (extended protocol). It writes quickdie's notice on an immediate stop, or the FATAL
  when the error is a 57014 cancel and Run's context is done. It then returns `errShutdownSent`, which closes the
  connection. Any other error raised during a stop is still the statement's own.
- `Server.runCtx` tells a server stop apart from a session-only cancel such as a terminate or a disconnect.

**Pins:** `TestStopImmediateSendsQuickdieNotice`, `TestFastStopSendsAdminShutdownFatal` and
`TestShutdownReplacesStatementCancel`. All three are mutation-checked. A live psql probe of idle and busy sessions,
simple and `\bind`, prints PG 18's lines for both stop modes.

**Still open (ledgered):** the immediate path still waits for session goroutines.

## Update 2026-10-30 — the walreceiver's connection preamble (M0122-0013, `7a5f06436`)

PG's WalReceiverMain runs these steps on every connection before it streams. goopg's walreceiver used to send
START\_REPLICATION straight after the startup handshake.

1. **IDENTIFY\_SYSTEM** (`identifySystem`):
   - It applies libpqrcv\_identify\_system's result-shape check (one row, at least three fields).
   - The primary's `systemid` must equal the standby's pg\_control identifier. Otherwise: "database system
     identifier differs between the primary and standby".
   - The primary's timeline must not be behind the start timeline. Otherwise: "highest timeline %u of the primary
     is behind recovery timeline %u".
   - `WalReceiverConfig.SystemID` = 0 skips the identifier check. Only in-process tests leave it zero; `goopg
     start` passes `LoadOrCreateSystemID`.
2. **WalRcvFetchTimeLineHistoryFiles** (`fetchTimelineHistoryFiles`): for every timeline from the start timeline to
   the primary's that pg\_wal lacks, it runs TIMELINE\_HISTORY. It checks the result shape and file name, as
   libpqrcv\_readtimelinehistoryfile does, and writes the content verbatim (`xlog.WriteHistoryContent`). An
   existing file is left alone. Timeline 1 has none.
3. **START\_REPLICATION**: the command is libpqrcv\_startstreaming's, `START_REPLICATION SLOT "name" X/X TIMELINE
   n`. The slot name is quoted, there is no PHYSICAL keyword, and the start timeline is always given.
   `WalReceiverConfig.StartTLI` defaults to the WAL writer's timeline.

Two related changes:

- **Promotion's timeline.** Standby promotion took the old timeline plus one. It now takes StartupXLOG's
  `findNewestTimeLine(recoveryTargetTLI) + 1` over pg\_wal (`xlog.FindNewestTimeline`). Fetching the history files
  is what makes this work: a standby on timeline 1 whose primary already reached timeline 3 is promoted onto
  timeline 4.
- **Walsender timeline.** IDENTIFY\_SYSTEM, READ\_REPLICATION\_SLOT and the START\_REPLICATION timeline check now
  read the WAL writer's live timeline (`currentTimeline`). They used to read the copy taken at startup, which
  stayed on the old timeline after an in-process promotion. With the new behind-timeline check, that stale value
  would have refused a downstream standby.

**Pins:**

- `TestWalReceiverFetchesMissingTimelineHistory` (the primary's writer is on timeline 3, and its Config.Timeline is
  unset);
- `TestWalReceiverIdentifySystemChecks`;
- `TestBuildStartReplicationCommand`;
- `TestStandbyControllerPromoteSkipsKnownTimelines`.

Each fails when its behaviour is disabled. The PG 18 → goopg failover E2E shows that a PG walsender accepts the
new command and the identifier check.

**Still open (ledgered):**

- The end-of-timeline handshake (CopyDone, then the next-timeline result set), on both the walsender and the
  walreceiver.
- The standby's writer switch and replay onto the child timeline, per recovery\_target\_timeline.
- `.done` markers for fetched history files.
- TIMELINE\_HISTORY of a missing file: goopg returns an empty row, where PG raises an error.
