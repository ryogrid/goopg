# Multi-timeline START_REPLICATION + timeline_id / pg_control reconciliation

**Status:** accepted
**Date:** 2026-08-09
**Milestone:** M0130 (S8)

## Problem

Three timeline-related gaps block PG physical replication:

1. **Hardcoded single timeline:** `replyStartReplication` in
   `internal/server/replication.go:427` rejects `TIMELINE n` for n > 1
   ("v0 is single-timeline"). `IDENTIFY_SYSTEM` at line ~161 returns
   hardcoded `"1"`.

2. **`global/timeline_id` divergence:** goopg stores the current TLI in a
   goopg-specific file `global/timeline_id` (4-byte LE uint32). PG stores
   TLI in pg_control's `checkPointCopy.ThisTimeLineID`. On promotion,
   these could diverge if only one is updated.

3. **Promotion TLI bump:** `cmd/goopg/standby.go finalizePromotion` bumps
   TLI and writes the history file, but the running WAL writer keeps the
   old TLI for the process lifetime (mid-stream segment renaming deferred).

## Design

### TLI source of truth (S8.1)

- **Single source:** pg_control `CheckPoint.ThisTimeLineID` is the
  authoritative TLI at all times.
- `global/timeline_id` is written on promote as a secondary copy (for fast
  read at goopg startup without pg_control CRC verification).
- On any mismatch at startup, pg_control wins; `global/timeline_id` is
  overwritten.
- `LoadOrCreateTimelineID` in `internal/initdb/timeline.go` reads pg_control
  first, falls back to `global/timeline_id`, falls back to
  `BootstrapTimeLineID` (1).

### IDENTIFY_SYSTEM (S8.2)

- Remove hardcoded `"1"` at `replication.go:161`.
- Read TLI from the current pg_control's `CheckPoint.ThisTimeLineID`.
- Return the real TLI as a string.

### START_REPLICATION TIMELINE n (S8.3)

- Remove the single-timeline rejection at `replication.go:427`.
- Accept `TIMELINE n` where n ≤ current TLI (the standby is catching up
  within the current timeline history).
- Reject n > current TLI (the standby's requested timeline is in the future).
- For TIMELINE = current TLI: stream from requested LSN (existing behavior).
- For TIMELINE < current TLI: stream the older timeline's WAL up to the
  switch point, then serve the history file (PG's expected behavior).

### TIMELINE_HISTORY (S8.4)

- `replyTimelineHistory` already serves history files. Verify it serves the
  correct file for any requested TLI ≤ current TLI.

### Promotion (S8.5)

- `finalizePromotion` in `cmd/goopg/standby.go`:
  1. Drain WAL to current LSN.
  2. newTLI = oldTLI + 1.
  3. Append history entry: `oldTLI \t switch_lsn \t "promoted"`.
  4. Write `<newTLI>.history` file (atomic write).
  5. Update pg_control `CheckPoint.ThisTimeLineID` = newTLI.
  6. Write `global/timeline_id` = newTLI.
  7. Remove `standby.signal`.
  8. Restart WAL writer with new TLI (segment naming changes to
     `0000000<N>...`).

## Guards

1. Multi-timeline failover E2E: goopg primary → PG standby → promote PG →
   goopg re-attaches on TLI 2.
2. IDENTIFY_SYSTEM returns the correct TLI after promotion.
3. TIMELINE_HISTORY serves the promoted timeline's history.
4. UNITS + `TestE2E_FailoverGoopgToPG` green.

## References

- `internal/server/replication.go:161` — IDENTIFY_SYSTEM hardcoded TLI
- `internal/server/replication.go:427` — single-timeline rejection
- `internal/initdb/timeline.go` — `LoadOrCreateTimelineID`, `WriteTimelineID`
- `internal/wal/timeline_history.go` — `ReadHistory`, `WriteHistory`
- `cmd/goopg/standby.go` — `finalizePromotion`
- `postgres/src/backend/access/transam/xlog.c` — timeline management

## What was built (2026-08-09)

### S8.1 — TLI source of truth

`LoadOrCreateTimelineID` (`internal/initdb/timeline.go`) now reads pg_control first
via `control.ReadControlFile`. Resolution order:
1. pg_control `CheckPointCopy.ThisTimeLineID` (authoritative).
2. `global/timeline_id` flat file (fallback when pg_control is absent or CRC-fails).
3. `BootstrapTimeLineID` (1).

If both exist and disagree, pg_control wins and the flat file is corrected.
`control.ReadControlFile` gained CRC32C validation so a corrupt or placeholder
pg_control (e.g. a test stub) is rejected, forcing fallback to the flat file.

### S8.2 — IDENTIFY_SYSTEM

`server.Config.Timeline` (uint32) added and wired at startup from
`LoadOrCreateTimelineID`. `replyIdentifySystem` returns the real TLI instead of
hardcoded `"1"`. Zero means "use TLI=1" for backward compat.

### S8.3 — START_REPLICATION TIMELINE n

Multi-timeline validation replaces the old single-timeline rejection. Rules:
- n=0: defaults to current TLI.
- n ≤ current TLI: accepted.
- n > current TLI: rejected (future timeline).
- n < current TLI: accepted (standby catching up from older timeline).

### S8.3x — WAL segment naming

`FormatSegmentNameTLI(segNo, tli)` added to `internal/wal/format.go`. The writer's
segment-creation path (`openSegment`, `eagerPreallocSegment`, `reclaimOldSegments`)
uses the writer's stored TLI; after promotion, new segments get the bumped TLI.
`Writer.TimelineID()` accessor added. `openSegmentFile` helper in `reader.go` is
TLI-tolerant: tries TLI=1 first, then scans for any segment matching the log+seg
suffix. `readStreamFrom` (used by `ReadAll`) updated to use this helper so
recovery and tests find segments regardless of TLI.

### S8.5 — Promotion pg_control update

`finalizePromotion` (`cmd/goopg/standby.go`) now calls `control.UpdateControlFile`
to set `CheckPointCopyThisTLI = newTLI` and `CheckPointCopyPrevTLI = oldTLI` after
writing the flat file. Mirrors upstream's `CreateEndOfRecoveryRecord`.

### Deferred

- Reader functions `detectWritePos` / `scanLastSegmentEnd` still use TLI=1
  segment naming — they run during initial WAL detection when TLI is always 1.
- Full multi-timeline WAL serving (stream TLI < current up to the switch point)
  is structurally supported but not yet exercised end-to-end.
