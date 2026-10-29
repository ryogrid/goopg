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
- TLI-aware segment fetch (`FormatSegmentNameTLI` currently hardcodes TLI=1 in the fetch loop)

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
