# 0149-0017 — Port recovery/t/026\_overwrite\_contrecord.pl

Status: done. `TestPort_Recovery026OverwriteContrecord`
\(`internal/testport/recovery_026_overwrite_contrecord_test.go`\) passes in all three pairings.

## What the test does

1. Fill the current WAL segment until less than two pages remain.
2. Emit a 600 kB logical message, so a record starts near the end of one segment and continues into the next.
3. Stop immediately and delete the segment holding the continuation.
4. Take a cold file copy \(`backup_fs_cold`\) as the standby.
5. Restart the primary. Its crash recovery finds the record's continuation missing, sets EndOfLog to the missing
   page \(`missingContrecPtr`\), and writes `XLOG_OVERWRITE_CONTRECORD` there on a page flagged
   `XLP_FIRST_IS_OVERWRITE_CONTRECORD` \(xlog.c `CreateOverwriteContrecordRecord`\).
6. The standby reads the partial record from its copy, meets the flag on the next page, restarts at that page
   \(xlogreader.c\), checks `overwritten_lsn`, and logs "successfully skipped missing contrecord at …"
   \(xlogrecovery.c `xlogrecovery_redo`\). It must then catch up and promote.

Pairings: all-goopg, PG 18.3 primary with a goopg standby, goopg primary with a PG 18.3 standby.

## Slice 1 \(landed\)

- `wal_keep_size` \(MB, SIGHUP\): registered, and the retainer applies `KeepLogSeg`'s clamp. At least
  `wal_keep_size / segment size` segments behind the checkpoint record's segment are kept \(`applyWalKeepSize`\).
  The value is read at start; a reload does not re\-apply it, the same as `max_slot_wal_keep_size`.
- PL/pgSQL expressions are `SELECT <expr>` \(pl\_exec.c `exec_eval_expr`\). A source that is not a plain expression,
  such as `setting::int FROM pg_settings WHERE …`, is parsed as the scalar subquery `(SELECT <expr>)`
  \(`parsePLExpr`\). PG errors when such a query returns more than one row; a scalar subquery's error text differs.
- `pg_walfile_name(pg_lsn)`: `XLByteToSeg` \(PG 18; earlier releases used the previous segment\) on the insert
  timeline. It refuses to run during recovery.
- tapnode `BackupFsCold`.

## Slice 2 \(0017a, landed\)

- **End of WAL at a missing segment.** In crash recovery only \(not as a standby, not under archive recovery\), a
  segment missing after the redo segment ends the WAL. The segment files past it are removed
  \(`RemoveSegmentsAfterGap`\), because goopg's reader and writer locate the stream by the files present:
  `liveSegmentRunStart` keeps the last contiguous run. Before this, a stale pre\-allocated segment past the hole made
  recovery replay nothing from the redo segment, which lost the test's whole `filler` table, and resume writing in the
  stale file. PG recycles such files and overwrites them.
- **Overwrite record.** `FindMissingContrecord` reproduces xlogreader.c's assembled\-record failure: a record whose
  header is on its first page and whose continuation page is absent, zero, or not its continuation. That gives
  `abortedRecPtr` and `missingContrecPtr`. Before replay and before the WAL writer opens,
  `WriteOverwriteContrecord` writes a page header flagged `XLP_FIRST_IS_OVERWRITE_CONTRECORD` and
  `XLOG_OVERWRITE_CONTRECORD` \(`overwritten_lsn`, `overwrite_time`, xl\_prev = the last complete record\) and fsyncs
  it. PG does this after replay; the order does not matter, because replay stops at the incomplete record either way.
- **Resume scan.** `scanLastSegmentEnd` drops a record that runs onto a flagged page and restarts there, as
  xlogreader.c does, so the writer resumes after the overwrite record. PG 18.3 `pg_waldump` decodes the result
  \(unit test\).

## Slice 3 \(0017b, landed\)

- **Reader restart.** The streaming reader \(`RecordIterator.readOneAt` → `overwritePageAhead`\) and the crash\-replay
  reader \(`readAllPageAware` → `overwritePageWithin`, its sibling\) check every already\-received continuation page
  of a multi\-page record. A page whose header carries `XLP_FIRST_IS_OVERWRITE_CONTRECORD`, with its own page address,
  drops the partial record. Reading restarts at that page, and the dropped record's start is handed to the next
  `Record` as `OverwrittenRecPtr` \(xlogreader.c `overwrittenRecPtr`\).
- **Verify and log.** Before applying a record, both replay loops run `verifyOverwriteContrecord`
  \(xlogrecovery.c `xlogrecovery_redo`\). `overwritten_lsn` must equal `OverwrittenRecPtr`, else replay stops with
  "mismatching overwritten LSN". Then it logs "successfully skipped missing contrecord at %X/%X, overwritten at %s",
  the time in the ISO `timestamptz_to_str` form.
- The PG\-primary / goopg\-standby pairing passes.

## Slice 4 \(0017d, landed\): records across segment boundaries

- **Layout.** PG's `ReserveXLogInsertLocation` reserves usable bytes with no segment rule. A record continues on the
  next segment's first page, whose long header carries `XLP_FIRST_IS_CONTRECORD` and `xlp_rem_len`; only `XLOG_SWITCH`
  pads to the boundary. goopg had instead re\-landed a crossing record at the boundary after an `XLOG_NOOP` pad, in
  both append paths: the stripe reservation `reserveEmittedAndPublish`, and Path A since M0131\-S30.6, which had
  copied it believing it was upstream's rule. Both now reserve straight across.
  - Nothing else depended on the pad: `writeAt` splits per segment, the header schedule \(`pageHeaderSizeAt`\) already
    emits the long header mid\-record, and the readers already read PG streams that do this.
  - The old layout stays behind `crossSegmentPadOn` \(test\-only\), because WAL written that way is still on disk.
- **Standby start.** A cold copy of a crashed primary has the same stale pre\-allocated segment past the hole. The
  standby had started replay there, skipping the redo segment. The end of local WAL at a missing segment now applies
  at standby start too \(not under archive recovery\), and the primary streams the rest.
- **Hardening found by 039\_end\_of\_wal:**
  - `FindMissingContrecord` validates a header that lies wholly on its page first, as XLogDecodeNextRecord does
    \(`ValidXLogRecordHeader`: rmid, an exact prev\-link\). A broken header is an ordinary end of WAL;
  - the overwrite\-page check runs on the raw `xl_tot_len` before header validation, in all three readers;
  - `readAllPageAware` used a stream\-relative position for absolute page addresses;
  - the end\-of\-WAL message is computed before the overwrite record changes the files.

## Not covered

- The skip line's time uses the process local zone, not `log_timezone`. An overwritten\-LSN mismatch is a replay error,
  not a FATAL process exit.
- `wal_keep_size` is not re\-applied on reload.
