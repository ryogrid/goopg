# recovery.signal — archive recovery via restore_command

**Status:** accepted
**Date:** 2026-08-09
**Milestone:** M0130 (S9)

## Problem

`recovery.signal` is recognized by goopg's startup code
(`internal/initdb/standby.go:35`) but the archive recovery mode is
unimplemented. PG 18.3 uses `recovery.signal` (or `standby.signal`) to
trigger WAL recovery at startup. The file's presence in a PG-created data
dir (e.g. after `pg_basebackup`) must trigger WAL replay.

## Design

### recovery.signal mode

When `recovery.signal` exists at startup (and `standby.signal` does NOT):

1. **Enter archive recovery:** read WAL segments from the archive location
   via `restore_command`, replay them, then promote to a new timeline.

2. **restore_command GUC:** register `restore_command` as a GUC (string,
   default empty, `ContextPostmaster`). When set, goopg shells out to the
   command to fetch each requested WAL segment.

3. **Segment fetch loop:**
   - For each WAL segment from the last checkpoint's REDO point onward:
     - Try to find it in `pg_wal/` first.
     - If missing, invoke `restore_command %f %p` where `%f` = segment
       filename, `%p` = destination path in `pg_wal/`.
     - If `restore_command` returns non-zero, wait and retry (with
       `archive_cleanup_command` support later).
   - Replay all fetched segments via `StreamReplayer` (existing).

4. **End of recovery:**
   - When no more WAL is available (archive exhausted and no running
     primary to stream from), promote:
     - `finalizePromotion` flow (new TLI, history file, update pg_control).
     - Remove `recovery.signal`.
   - The server becomes a writable primary.

### Promotion at end of recovery (S9.3)

- Reuses the existing `finalizePromotion` from `cmd/goopg/standby.go`.
- The difference from standby promotion: no live streaming connection
  to drain — just replay the last fetched segment and promote.

### Deferred / optional

- `recovery_target_time` / `recovery_target_xid` / `recovery_target_name`
  (point-in-time recovery targets). These are PG features that can be added
  incrementally — the core loop (fetch → replay → promote) is the M0130 scope.
- `archive_cleanup_command`.

## Guards

1. `recovery.signal` present at startup → archive recovery mode engaged.
2. `restore_command` succeeds → WAL segments fetched and replayed.
3. Archive exhausted → server promotes to writable primary on a new TLI.
4. UNITS + archive-recovery guard test green.

## References

- `internal/initdb/standby.go:35` — signal file recognition
- `cmd/goopg/standby.go` — `finalizePromotion`
- `internal/wal/stream_replayer.go` — `StreamReplayer`
- `postgres/src/backend/access/transam/xlogarchive.c` — `RestoreArchivedFile`
- `postgres/src/backend/access/transam/xlogrecovery.c` — recovery loop

## What was built (2026-08-09, M0130-S9)

### New files

| File | What |
|------|------|
| `internal/wal/archive_restore.go` | `RestoreArchivedFile(restoreCommand, walDir, segmentName)` — shells out to `restore_command` with `%f`/`%p` substitution, mirrors upstream `BuildRestoreCommand` + `system()` pattern |
| `internal/wal/archive_recovery.go` | `RunArchiveRecovery(mgr, dataDir, restoreCommand, segmentSize)` — archive recovery loop: discovers highest local segment, fetches missing ones via `RestoreArchivedFile`, reads `ReadSegmentRecords`, replays via `ReplayRecords`, exits when archive exhausted |
| `internal/wal/archive_recovery_test.go` | Unit tests for `highestLocalSegment`, `RestoreArchivedFile` |

### Modified files

| File | What |
|------|------|
| `internal/config/defaults.go` | Registered `restore_command` GUC (string, default `""`; `ContextPostmaster` at first, PGC\_SIGHUP since 2026-10-29 — see the update below) |
| `internal/initdb/standby.go` | Added `IsRecovery()`, `RemoveRecoverySignal()` — mirror `IsStandby()`/`RemoveStandbySignal()` for `recovery.signal` |
| `internal/initdb/open.go` | Added `Recovery bool` field to `Runtime`; check for `recovery.signal` at Open time |
| `cmd/goopg/main.go` | Archive recovery path at startup: when `recovery.signal` present (and `standby.signal` absent), call `RunArchiveRecovery` → `promoteAfterRecovery` → become primary |
| `cmd/goopg/standby.go` | Added `promoteAfterRecovery()` — TLI bump + history write + pg_control update + signal removal, mirrors `finalizePromotion` without the standby controller |

### Deferred

- `recovery_target_time` / `recovery_target_xid` / `recovery_target_name` (PITR targets)
- ~~`archive_cleanup_command`~~ — landed 2026-10-29 (M0122-0013; design 0005-0001)
- `recovery_target_timeline` beyond "latest"
- ~~TLI-aware segment fetch (`FormatSegmentNameTLI` hardcoded TLI=1 in the fetch loop)~~ — landed 2026-10-29 (`173eca961`, below)

## Updates (M0122-0013, 2026-10-29)

- **Start-up validation.** `validateRecoveryParameters` (xlogrecovery.c) is ported: `recovery.signal` without
  `standby.signal` and without `restore_command` is FATAL, and the signal file stays. Before this, goopg deleted the
  signal file and started as a primary. `restore_command` is PGC\_SIGHUP (`04b33fccf`).
- **`recovery_end_command`** runs at the end of archive recovery, inside `promoteAfterRecovery` and before
  `recovery.signal` is removed (`7911ddeda`).
- **Timeline switch point = EndOfLog** (`69c31e230`). PG writes the new timeline's history file with EndOfLog, the end
  of the last replayed record (xlog.c `StartupXLOG` → `writeTimeLineHistory`). `promoteAfterRecovery` used the
  running writer's `WrittenLSN`, which is the local end from before archive recovery. So whenever `restore_command`
  supplied segments, the history file named a switch point earlier than the WAL actually replayed.
  - `RunArchiveRecovery` now returns `endOfLog`: the `EndLSN` of the last record it replayed past the local end, or 0
    when the archive supplied nothing.
  - `promoteAfterRecovery` uses the later of `endOfLog` and the writer's end.
  - Pins: `TestRunArchiveRecoveryFetchesThenStops` (`endOfLog` equals the archived record's end) and
    `TestPromoteAfterRecoveryHistorySwitchPoint` (restored vs nothing restored).
- **The writer adopts the restored WAL and switches timeline** (`02bf60de2`; P0-E17, S2 data loss).
  - **Defect:** after archive recovery the running writer still sat at its pre-archive end on the old timeline. A
    probe showed the first post-promotion record written at `0x3000029`, inside replayed WAL ending at `0x4001060`, on
    TLI 1. That record overwrote WAL whose changes were already in the data pages.
  - **Fix:**
    - `RunArchiveRecovery` also returns the last record's start.
    - `adoptRestoredWAL` feeds the restored byte range [writer end, EndOfLog) through `Writer.AppendRaw`. That is the
      walreceiver's path, and it writes the identical bytes back into the same files. The writer then stands where a
      streaming standby's would.
    - `switchWriterToNewTimeline` is factored out of the streaming promotion, and both promotions call it. It runs
      `SwitchTimeline` at EndOfLog, writes `XLOG_END_OF_RECOVERY`, flushes, and sets `minRecoveryPoint`, as PG's
      `StartupXLOG` (`XLogInitNewTimeline`, `CreateEndOfRecoveryRecord`) does.
  - **Pin:** `TestPromoteAfterRecoveryAdoptsRestoredWAL` restores real archived segments, then checks:
    - the restored segment is byte-identical after promotion;
    - the history file names EndOfLog;
    - the writer is on the new TLI, and new WAL starts at EndOfLog or later;
    - a restart reopens on the new TLI.
    It fails if the adoption is removed.
- **Still open:**
  - The fetch loop still hardcodes TLI 1 (above).
  - goopg has no WAL archiver (`archive_mode` / `archive_command`, `.ready` / `.done`). So PG's
    `RemoveNonParentXlogFiles` exception for `.ready` segments and the old timeline's `.partial` rename, which needs
    `XLogArchivingActive()`, have nothing to act on.

## Update 2026-10-29 — timeline-following archive recovery (M0122-0013, `173eca961`; P0-E18)

**Defect (S2, data loss).** The fetch loop named segments on TLI 1 only. Restoring a base backup of a server that had
been promoted, recovery stopped at the timeline switch and promoted from there, silently dropping every record written
on the later timeline. A mutation run of the new test shows exactly that: recovery ended on TLI 1 at the switch point.

**PG behaviour** (`recovery_target_timeline = 'latest'`, the default; xlogrecovery.c):
- `findNewestTimeLine` probes `<tli>.history` files upward from the start timeline.
- `readTimeLineHistory` turns the newest one into the expected timelines.
- `XLogFileReadAnyTLI` reads each segment from the newest expected timeline that has it and began at or before it, and
  never goes back to a timeline older than the last segment's.

**Ported:**
- `RunArchiveRecovery` returns `ArchiveRecoveryResult`: `EndOfLog`, `LastStart`, `EndTLI`, `NewestTLI` and
  `Timelines`.
  - The start TLI comes from pg\_control.
  - History files are probed locally or via `restore_command`, and restored into pg\_wal.
  - The start timeline must appear on the newest timeline's history. Otherwise recovery fails with PG's "not a child of
    this server's history" error.
  - The loop starts at the highest local segment, not after it, so a newer timeline's copy of the switch segment is
    read.
- `adoptRestoredWAL` crosses each timeline switch in the restored range the way a following standby does: it appends
  up to the timeline's begin, runs `SwitchTimeline` there, and goes on. It reads the restored bytes of the switch
  segment before the switch zero-fills them. It also appends one segment at a time, which removes the previous
  whole-range buffer.
- `promoteAfterRecovery` takes the parent timeline as `EndTLI` and the new timeline as `NewestTLI + 1`, which can't
  collide with any timeline in the archive (`StartupXLOG`). The history becomes 1 → 2 → 3.

**Pins:** `TestRunArchiveRecoveryFollowsTimelineHistory` and `TestPromoteAfterRecoveryFollowsTimelines`. The second
takes a base backup on TLI 1, promotes the primary to TLI 2, then checks that recovery plus promotion end on TLI 3 with
the history chain and survive a restart. Both fail when the history is ignored.

**Still open (ledgered):**
- Only 'latest' is supported; an explicit `recovery_target_timeline` or 'current' is not.
- Local WAL that extends past a timeline switch is refused, where PG would read the newer timeline from that point.
- Restored parent history files stay in pg\_wal; PG reads them to a temporary name.

## Update 2026-10-29 — data-directory lock and SIGHUP during recovery (M0122-0013, `ca0bde402`; P0-E19)

**The defect (S2, P0-E19).** goopg wrote `postmaster.pid` only once the listener was bound, and nothing read it at
start. A second `goopg start` on the data directory of a live server, on another port, therefore:

- ran crash recovery against the live server's WAL and wrote a checkpoint into it;
- overwrote the owner's `postmaster.pid`;
- served connections;
- deleted the owner's lock file when it stopped.

PG refuses at once in `CreateDataDirLockFile` (miscinit.c `CreateLockFile`).

**The port.**
- `control.CreateDataDirLockFile` follows `CreateLockFile` with `isDDLock`:
  - It creates the file with `O_EXCL`, retrying up to 100 times.
  - It refuses when the existing file names a live process, with PG's errmsg and errhint: `lock file
    "postmaster.pid" already exists` and `Is another postmaster (PID n) running in data directory "…"?`. A negative
    pid gives the `postgres` hint.
  - An empty file is refused with "is empty" and a non-numeric one with "bogus data".
  - A dead pid, our own pid, our parent's or `PG_GRANDPARENT_PID` is stale: the file is unlinked and the create
    retried.
  - Upstream's orphaned-shared-memory probe has no counterpart, because goopg backends are goroutines of the server
    process.
- `goopg start` takes the lock before `initdb.Open`, so before crash and archive recovery. A missing directory is still
  reported by `Open`. The file first holds only the pid, the data directory and the start time; `WritePIDFile` fills in
  the listen address and socket path when the listener binds. On exit, `RemoveDataDirLockFile` unlinks the file only
  while it still names this process.
- `ParsePIDFile` keeps the empty trailing lines of a startup-phase lock file.

**SIGHUP during recovery.** With the pid now discoverable during recovery, `pg_ctl reload` (a `kill -HUP`) would have
hit Go's default action for SIGHUP and terminated the process.
- `startupReload` registers SIGHUP before the lock is taken. Its signal goroutine only sets a flag, as the startup
  process's handler sets `got_SIGHUP`.
- `process` applies `ProcessConfigFile(PGC_SIGHUP)` synchronously:
  - at the archive-recovery fetch boundary (`ProcessStartupProcInterrupts`);
  - once more before the server starts, after which the server's own handler takes over.
- `RunArchiveRecoveryWith` reads `restore_command` before every fetch, as `RestoreArchivedFile` reads
  `recoveryRestoreCommand` on each call.
- `goopg reload` sends SIGHUP directly to a server whose socket line is still empty, as `pg_ctl reload` always does.
- `misc.ReloadConfigEntries` reads postgresql.conf and then postgresql.auto.conf for both the server's reload and the
  startup phase.

**Pins:**
- `TestCreateDataDirLockFile` and `TestRemoveDataDirLockFileOnlyOwn`.
- `TestStartRefusesLiveDataDirLock`: the FATAL and HINT text; the owner's lock file and pg\_control are left unchanged.
- `TestStartRecoverySignalWithoutRestoreCommandIsFatal`: the lock is released on a FATAL exit.
- `TestStartupReloadAppliesSIGHUP` and `TestRunArchiveRecoveryWithReadsRestoreCommandPerFetch`.
- Each mechanism was mutation-checked.
- A live probe sent `goopg reload` during a slow `restore_command`. The reload was applied at the next fetch, which then
  used the new command.

**Still open (ledgered):**
- The lock file keeps goopg's 5-line format, not PG's 8-line `postmaster.pid` (port, socket directory, listen address,
  shared-memory key, status line).
- SIGTERM/SIGINT during start-time recovery cancels only the context. Recovery runs to the end, and `goopg stop` cannot
  reach a server that has no control socket yet.

## Update 2026-10-29 — shutdown during archive recovery and FATAL restore failures (M0122-0013, `7d2f398fa`; P0-E20)

**The defects.** goopg read every `restore_command` failure as the end of the archive, and nothing in start-time archive
recovery looked at SIGTERM/SIGINT.
- **P0-E20 (S2, data loss):** a mistyped command (exit 127, "command not found") or a child killed by a signal ended
  recovery at the local WAL end. goopg then promoted to a new timeline and abandoned the rest of the archive. PG
  reports these at FATAL precisely so that recovery is not assumed complete.
- **Fast shutdown:** a SIGTERM/SIGINT was ignored until the archive was exhausted. goopg then promoted, and exited only
  afterwards.

**The port (`RestoreArchivedFile`, xlogarchive.c; `StartupProcShutdownHandler` / `ProcessStartupProcInterrupts`,
startup.c).**
- `RestoreArchivedFileContext` classifies failures as upstream does:
  - a child killed by SIGTERM, or the shell's exit 128\+15, returns `ErrRecoveryShutdown`;
  - any other signal, or an exit status above 125, returns `RestoreFatalError` with `wait_result_to_str`'s text
    ("command not found", "child process was terminated by signal 9: Killed", …);
  - anything else is the normal end of the archive.
- The command expands `BuildRestoreCommand`'s `%f`, `%p`, `%r` and `%%`; `%r` and `%%` used to be left verbatim.
- A cancelled context sends SIGTERM to the command's process group, the way the postmaster signals the startup
  process's group.
- `RunArchiveRecoveryWith` takes the shutdown context and checks it before every fetch and every segment replay. It
  returns shutdown and FATAL errors instead of reading them as an exhausted archive.
- `endInterruptedArchiveRecovery` leaves recovery unfinished: no promotion, `recovery.signal` kept, and no shutdown
  checkpoint, because a server in recovery writes no checkpoint record.
  - A shutdown request stamps `DB_SHUTDOWNED_IN_RECOVERY` and exits 0.
  - Anything else is FATAL with exit 1, as when the startup process dies FATAL.
- `goopg stop` signals a server whose lock file has no control socket yet: SIGINT for fast, SIGTERM for smart, as
  `pg_ctl stop` does.

**Pins:**
- `TestRestoreArchivedFileFailureClassification`, `TestRestoreArchivedFilePlaceholders` and
  `TestRestoreArchivedFileShutdownKillsCommand`.
- `TestRunArchiveRecoveryStopsOnFatalRestoreAndShutdown`.
- `TestStopDuringArchiveRecoveryLeavesRecoveryUnfinished`: stop during a slow `restore_command` leaves no new
  timeline, `recovery.signal` kept and pg\_control in "shut down in recovery"; the next start resumes recovery and
  promotes.
- Four mutations fail these tests: context not passed, signal classification off, group kill off, state not stamped.
- Live probes:
  - `goopg stop` returned in 60 ms, and `pg_controldata` reports "shut down in recovery".
  - A missing command is FATAL, with no promotion and `recovery.signal` kept.

**Still open (ledgered):**
- `initdb.Open`'s crash recovery ignores a shutdown request.
- `-mode immediate` cannot stop a server that is still starting, because SIGQUIT is unhandled.
- The shutdown does not adopt the last replayed checkpoint as a restartpoint.
- pg\_control shows `DB_IN_PRODUCTION`, not `DB_IN_ARCHIVE_RECOVERY`, during recovery.signal recovery.
- There is no `RECOVERYXLOG` temporary name and no wrong-size check on a restored file.
- pg\_wal is read before the archive.

## Update 2026-10-29 — archive before pg\_wal, RECOVERYXLOG, recovery.signal is archive recovery (M0122-0013, `58b4511c2`; P0-E21)

**The defect (P0-E21, S2 — data loss).** A base backup's last local segment holds WAL only up to the backup end; the
archive's copy of the same segment also holds what the primary wrote afterwards. goopg read the local copy first.
- It replayed that segment only up to the backup end, then went on to the next segments from the archive. Everything
  in between was skipped.
- `EndOfLog` still landed at the true end, so nothing looked wrong. The promotion then built the new timeline on a
  history with a hole in it.
- Opening the data directory also wrote an end-of-crash-recovery checkpoint record at the local end. That spot is where
  the archived copy holds the primary's WAL.

**The port.**
- `have()` follows `XLogFileReadAnyTLI`: XLOG\_FROM\_ARCHIVE is tried before XLOG\_FROM\_PG\_WAL. The local copy is used
  only when the archive lacks the file, because it "could be an old, un-filled or partly-filled version".
- `RestoreArchivedFileContext` follows `RestoreArchivedFile` and `KeepFileRestoredFromArchive`:
  - the command restores into `RECOVERYXLOG` (`RECOVERYHISTORY` for history files), after removing any leftover file of
    that name, so a failed restore never touches the local copy;
  - a segment of the wrong size is FATAL ("archive file … has wrong size");
  - the restored file is renamed over the pg\_wal copy and the directory is synced;
  - both temporary names are removed when recovery ends (`FinishWalRecovery`).
- `initdb.Open` treats recovery.signal as archive recovery (`ArchiveRecoveryRequested`):
  - pg\_control is `DB_IN_ARCHIVE_RECOVERY` from the start;
  - the checkpointer runs in recovery mode, so the end-of-crash-recovery checkpoint becomes a restartpoint flush and no
    WAL record is written before recovery ends.
- `promoteAfterRecovery` leaves recovery mode before its end-of-recovery checkpoint, then stamps `DB_IN_PRODUCTION`.
- With `wal_init_zero = off`, a new segment is still made full-size, as `XLogFileInitInternal` writes its last byte.
  goopg used to leave the file short, which the size check and external tools reject.

**Pins:**
- `TestArchiveRecoveryPrefersArchivedCopyOfPartialLocalSegment` covers the base-backup shape. After recovery, pg\_wal's
  tail segment is the archived copy and replays past the backup end. pg\_control goes from `DB_IN_ARCHIVE_RECOVERY` to
  `DB_IN_PRODUCTION`, and a restart lands on the new timeline. The test fails with pg\_wal preferred, and fails with
  Open's recovery mode off.
- `TestRestoreArchivedFileThroughRecoveryXLog` checks that a failed restore leaves the local copy alone, that a short
  restore is FATAL, and that a good restore replaces the local copy. It fails with the size check off.

**Still open (ledgered):**
- The writer keeps a descriptor for a tail segment that the restored copy replaces. This is correct only because the
  bytes are identical.
- Start-time archive recovery takes no restartpoints.
- Restored history files are kept in pg\_wal.

## Update 2026-10-29 — the writer reopens replaced segments (M0122-0013, `6001f288f`)

Archive recovery puts the archived copy of a segment in place by rename. The WAL writer opened by `initdb.Open`
still held a descriptor for the replaced local tail segment. Promotion's `adoptRestoredWAL` appends, and anything
else written there, therefore went to the orphaned inode. That was correct only because those bytes equalled the
archived copy. PG opens no WAL for writing until recovery ends.

**Fix:**
- `Writer.ReopenSegmentFiles` flushes and then drops every cached segment descriptor, quiesced like
  `SwitchTimeline`. The next write opens each segment by its path.
- `promoteAfterRecovery` calls it before it reads the writer's end and appends the restored WAL.

**Pin:** `TestReopenSegmentFilesWritesTheReplacedSegment` writes a record after the open segment was renamed over,
and checks that it is in pg\_wal's file. It fails when the descriptors are kept.

**Still open (ledgered):** start-time archive recovery takes no restartpoints, and restored history files are kept in
pg\_wal.

## Update 2026-10-29 — recovery\_target\_timeline 'current' and numeric targets (M0122-0013, `4caa86ca8`)

goopg registered `recovery_target_timeline` but always followed 'latest'.
- `misc.ParseRecoveryTargetTimeline` and the GUC's check hook port `check_recovery_target_timeline`:
  - "current" and "latest" are taken by name;
  - anything else is read as `strtoul(s, NULL, 0)` (prefix base, longest digit prefix, 0 when there is none);
  - only an overflow is rejected, with PG's errdetail.
- `RunArchiveRecoveryWith` takes the target, following `validateRecoveryParameters`:
  - 'current' follows pg\_control's timeline;
  - a numeric N follows N's history, and is FATAL "recovery target timeline N does not exist" when N≠1 has no history
    file;
  - for every target, the start timeline must be on the target's history.
- `NewestTLI` is still `findNewestTimeLine(target)`, so the promotion's new timeline cannot collide with an archived
  one. `TargetTLI` is reported.

**Pins:** `TestRunArchiveRecoveryTargetTimeline` and `TestParseRecoveryTargetTimeline` (mutation-checked).

**Still open (ledgered):**
- A standby follows only the newest timeline.
- An empty value reads as 'latest'.
- The "not a child" FATAL lacks PG's errdetail.

## Update 2026-10-30 — timeline history through RECOVERYHISTORY (M0122-0013, `0603610a5`)

goopg restored every probed history file into pg\_wal under its own name, including the target timeline's. PG handles
history during archive recovery like this:
- `readTimeLineHistory` and `existsTimeLineHistory` restore it into `RECOVERYHISTORY` and read it from there,
  without keeping it.
- `restoreTimeLineHistoryFiles(checkpoint TLI, recoveryTargetTLI)` copies into pg\_wal only the files for the timelines
  between the start and the target, not the target's own.

**The port.**
- `RestoreArchivedFileTemp` is `RestoreArchivedFile` without `KeepFileRestoredFromArchive`.
- `historyPath` restores into `RECOVERYHISTORY`, falling back to the pg\_wal copy. It serves `findNewestTimeLine`, the
  numeric-target existence check and the target's history.
- The target's entries are reported verbatim as `TargetHistory`.
- The `[start, target)` history files are kept in pg\_wal.
- `promoteAfterRecovery` copies the parent's history from `TargetHistory` when the parent is the target, as
  `writeTimeLineHistory` reads it through `RECOVERYHISTORY` too. Reading pg\_wal alone would now drop every ancestor
  line.

**Pins:**
- `TestRunArchiveRecoveryFollowsTimelineHistory` (updated): the target's history is not kept, and `TargetHistory` is
  verbatim.
- `TestRunArchiveRecoveryKeepsIntermediateHistoryFiles`: with timelines 1 → 2 → 3, timeline 2's file is kept and
  timeline 3's is not.
- `TestPromoteAfterRecoveryFollowsTimelines` catches a promotion that reads pg\_wal only.
- All three were mutation-checked.

**Still open (ledgered):** start-time archive recovery takes no restartpoints.
