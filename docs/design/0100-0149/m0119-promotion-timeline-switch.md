# M0119 — promotion switches the running WAL writer's timeline and writes XLOG_END_OF_RECOVERY

Status: landed 2026-10-21 (streaming-standby promotion; archive-recovery promotion ledgered).

## 1. Gap

`finalizePromotion` (cmd/goopg/standby.go) did three things:

- bumped the timeline in `global/timeline_id` and pg\_control;
- wrote `<newTLI>.history`;
- left the running WAL writer on the old timeline until a restart.

Reading the code turned up four defects:

1. **Post-promotion WAL carried the old TLI.** Segment names and page
   headers kept it, which contradicts the history file.
2. **The first checkpoint undid the promotion.** The checkpointer stamps
   pg\_control's TLI from `Writer.TimelineID()` (M0131-S18.3), so it wrote
   TLI 1 back. A restart then came up on TLI 1, even though
   `00000002.history` exists.
3. **The first new record's xl\_prev was stale.** `appendRaw` (the
   walreceiver path) resets the insert tracker's prev to the value before the
   received bytes. Their record boundaries are never tracked, so the first
   record goopg framed after promotion named a record from before streaming
   began. PG readers reject that as an incorrect prev-link.
4. **A partial tail made every post-promotion record unreadable.** When the
   primary's stream was cut mid-record, promotion stopped replay at the last
   complete record (`AtEndOfWAL`), but the writer kept appending after the
   partial bytes. Every reader stops at the partial record as end of WAL, so
   post-promotion commits were unreadable on restart. The rewind mutation in
   §5 reproduces this.

## 2. PG (xlog.c StartupXLOG, end of archive recovery)

1. `EndOfLog` is the end of the last valid record. WAL insertion resumes
   there; a partial tail is overwritten.
2. `XLogInitNewTimeline(endTLI, EndOfLog, newTLI)`:
   - when EndOfLog is inside a segment, `XLogFileCopy` copies that segment
     to the new timeline's name up to EndOfLog and zero-fills the rest;
   - at a boundary, the new timeline's first segment is created.
3. `CleanupAfterArchiveRecovery`:
   - `RemoveNonParentXlogFiles` removes old-timeline segments numbered above
     the switch segment;
   - the last old segment is renamed `.partial` only when archiving.
4. `CreateEndOfRecoveryRecord`:
   - inserts RM\_XLOG `XLOG_END_OF_RECOVERY` {end\_time, ThisTimeLineID,
     PrevTimeLineID, wal\_level} as the new timeline's first record;
   - flushes it;
   - sets pg\_control `minRecoveryPoint` to its end, with
     `minRecoveryPointTLI`.

## 3. Change

- **`Writer.SwitchTimeline(newTLI, endOfLog, prevRecPtr)`**
  (xlog/writer\_timeline.go). It holds `writeMu` and `appendMu`, with no
  append in flight, the same precondition as PG's "writing WAL isn't
  allowed yet". It then:
  - flushes what was received;
  - resets the insert tracker, the WAL buffer, the sender ring, the
    published watermark, and the write/drain/flush positions to endOfLog,
    with the tracker's prev set to prevRecPtr;
  - closes every open old-timeline segment;
  - copies the switch segment under a temporary name, then renames it into
    place (`copySegmentPrefix`);
  - removes later old-timeline segments (`removeSegmentsAfter`);
  - stores newTLI.
- **The writer's TLI is a live value.** `state.tli` is an `atomic.Uint32`.
  The stripe core's pad layout reads it through `padLayout.tliRef`
  (`timeline()`), so pads emitted after the switch carry the new TLI.
- **Readers open a segment's newest timeline.** The segment holding the
  switch point exists on both timelines.
  - `openSegmentFile` now picks the newest TLI for a segment number
    (`newestSegmentPath`); it used to try TLI 1 first, which after a switch
    would have read the old copy and missed post-switch WAL.
  - `RecordIterator` (walsender, standby replay) falls back to the newest
    timeline at or below the writer's when a segment exists only under an
    older name.
- **`XLOG_END_OF_RECOVERY`.** `EncodeEndOfRecoveryPG` builds the 24-byte
  body (sizeof, padding included); `DecodeEndOfRecovery` decodes it.
  `PGTimestampTz` converts the end time.
- **Promotion** (`finalizePromotion`), after the history and pg\_control TLI
  updates:
  - `endOfLog()` takes the end and start of the last record the replayer
    applied (`StreamReplayer.LastApplied`). With none applied, it takes the
    replay baseline and the writer's own prev (`Writer.InsertPosition`).
  - It calls `SwitchTimeline` there.
  - It appends and flushes the end-of-recovery record (wal\_level from the
    GUC).
  - It sets pg\_control `MinRecoveryPoint`/`MinRecoveryPointTLI`.
  - The history file's switch LSN is that same end of log.

## 4. Verified with PG's own tool

`TestE2E_FailoverPGtoGoopg`, in all three modes, now runs PG 18.3's
`pg_waldump --timeline 2 --start <history switch LSN> --limit 3` on the
promoted standby's pg\_wal. The first record is:

```
rmgr: XLOG len (rec/tot): 50/50, tx: 0, lsn: 0/03003988, prev 0/03003960,
desc: END_OF_RECOVERY tli 2; prev tli 1; time …; wal_level replica
```

- It sits exactly at the switch point, and pg\_waldump accepts its
  prev-link.
- The post-promotion insert's records decode after it.

## 5. Pins

- `TestSwitchTimelineResumesAtEndOfLog` (xlog). Setup: three records, then a
  partial raw tail, then the switch. It asserts:
  - the partial tail is dropped;
  - the next record starts at end of log, with xl\_prev naming the last
    record;
  - the new-TLI copy is a full segment and the old segment is kept;
  - a preallocated old-TLI segment past the switch is removed;
  - all four records read back.

  Mutations:
  - dropping the tracker reset puts the record after the partial tail, where
    it is unreadable (defect 4);
  - picking the oldest TLI in `newestSegmentPath` loses it on read-back.
- `TestEndOfRecoveryRecordRoundTrips` (encode ↔ decode, rmgr/info, 24-byte
  body).
- `checkPromotedTimelineWAL` in the failover E2E (§4).

## 6. Not done (ledgered)

- **Archive-recovery promotion** (`promoteAfterRecovery`). It restores
  segments into pg\_wal behind the running writer's back, so the writer has
  no end of log or prev-link to switch at. It still bumps TLI metadata only.
- **Archiving.** The old timeline's last segment is not renamed `.partial`
  when archiving is on, and goopg has no archiver integration here.
- **Recycling.** `RemoveNonParentXlogFiles` recycles up to 10 old segments
  into future new-timeline names; goopg removes them.
- **Promotion checkpoint.** PG requests one after the end-of-recovery
  record; goopg's checkpointer runs on its own schedule.

## Update 2026-10-29 — recovery.signal promotion (M0122-0013, P0-E17)

The archive-recovery promotion now goes through the same switch. `switchWriterToNewTimeline` (cmd/goopg/standby.go) is
shared by both promotions. Before it switches, `promoteAfterRecovery` brings the writer to EndOfLog with
`adoptRestoredWAL`, feeding the segments `restore_command` supplied through `Writer.AppendRaw`. Before this change
the writer resumed at its pre-archive end and overwrote restored, already-replayed WAL (S2). Details are in
`0130-0009-recovery-signal-archive-recovery.md`.

## Update 2026-10-29 — end-of-recovery checkpoint (M0122-0013)

PG ends recovery with a checkpoint (xlog.c):
- **Promotion:** `CreateEndOfRecoveryRecord`, then `StartupXLOG` requests an online checkpoint
  (`RequestCheckpoint(CHECKPOINT_FORCE)`).
- **Archive recovery ending without a promote trigger:** `PerformRecoveryXLogAction` waits for
  `CHECKPOINT_END_OF_RECOVERY | IMMEDIATE | WAIT`.

goopg's promotions used to wait for the checkpoint schedule. Until then pg\_control kept the last restartpoint on the
old timeline.

`promotionCheckpoint` (cmd/goopg/standby.go) now runs `CheckpointNow`:
- **Streaming promotion:** as its last step, after the `DB_IN_PRODUCTION` stamp.
- **recovery.signal promotion:** right after the timeline switch and before `recovery_end_command`, in PG's order.

Details:
- It is synchronous in both cases. PG's promoted request does not wait, but goopg's checkpointer has no request queue
  and does not serialise concurrent runs.
- A failure is a WARNING, as with the crash-recovery `endOfRecoveryCheckpoint`.
- It is skipped if the writer is still on the old timeline, because the checkpointer stamps the writer's TLI.

Pins: `TestStandbyControllerPromoteTakesCheckpoint`, and `TestPromoteAfterRecoveryAdoptsRestoredWAL` (checkpoint copy on
the new TLI, redo past EndOfLog). Both fail without the checkpoint.

Still different (ledgered):
- The recovery.signal path marks the timeline change with `XLOG_END_OF_RECOVERY`, where PG writes an end-of-recovery
  shutdown checkpoint. Both are valid timeline-changing records.
- The promoted checkpoint is waited for.

## Update 2026-10-29 — end-of-recovery checkpoint on the recovery.signal path (M0122-0013, `a75b43cf0`)

`PerformRecoveryXLogAction` (xlog.c) ends recovery in one of two ways:
- **Promotion triggered:** the lightweight `XLOG_END_OF_RECOVERY` record.
- **Archive recovery without a promote trigger:** `CHECKPOINT_END_OF_RECOVERY | IMMEDIATE | WAIT`. `CreateCheckPoint`
  treats it as "really a shutdown checkpoint, just issued at a different time": an `XLOG_CHECKPOINT_SHUTDOWN` on the
  new timeline whose `PrevTimeLineID` is the timeline recovery forked off from.

goopg's recovery.signal path wrote `XLOG_END_OF_RECOVERY`, followed by an ordinary checkpoint. Now:
- `checkpointOpts.endOfRecovery` / `prevTLI` select the shutdown record format: no `XLOG_CHECKPOINT_REDO`, no
  running-xacts, and `PrevTimeLineID = prevTLI`. pg\_control is left in `DB_IN_PRODUCTION`. (PG stamps
  `DB_SHUTDOWNED` here and `StartupXLOG` moves it to production right after, so the end state is the same.)
- `Checkpointer.CheckpointEndOfRecovery(prevTLI)` runs it immediately and waits.
- `endArchiveRecoveryOnNewTimeline` switches the writer and takes that checkpoint as the first record on the new
  timeline. It falls back to `XLOG_END_OF_RECOVERY` only when there is no checkpointer. A failed checkpoint fails the
  start, as an ERROR in PG's startup process does.
- `promotionCheckpoint` is now only the streaming promotion's request.

Pins:
- `TestCheckpointerEndOfRecoveryCheckpoint`: exactly one record, shutdown type, This/Prev TLI 3/2, in production; an
  ordinary checkpoint afterwards keeps Prev = This.
- Both archive-promotion tests assert that the first record on the new timeline is this checkpoint. They fail with
  `info 0x90` (`XLOG_END_OF_RECOVERY`) if the old path is restored.
