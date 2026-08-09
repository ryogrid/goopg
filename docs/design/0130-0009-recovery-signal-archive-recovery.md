# recovery.signal — archive recovery via restore_command

**Status:** draft
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
