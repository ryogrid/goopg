# 0149-0017 — Port recovery/t/026\_overwrite\_contrecord.pl

Status: in progress. `TestPort_Recovery026OverwriteContrecord`
\(`internal/testport/recovery_026_overwrite_contrecord_test.go`\) is gated by `GOOPG_TAP_026=1`.

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

## Open

- **0017a — goopg primary.** `detectWritePos` \(xlog/writer.go\) derives the resume position from segment files. A
  hole in the segment run is taken as a retention leftover and everything below it is dropped. Here that discards
  the segment holding redo and the torn record, and the primary resumes in the next pre\-allocated segment. The
  resume position must come from where recovery's reader stopped:
  - `abortedRecPtr` / `missingContrecPtr` from the assembled\-record error;
  - EndOfLog = `missingContrecPtr`, and xl\_prev = the last complete record;
  - then the overwrite record on a flagged page.
- **0017b — goopg standby.** The reader must restart at an `XLP_FIRST_IS_OVERWRITE_CONTRECORD` page while assembling
  a record, keeping `overwrittenRecPtr`. Replay must verify `overwritten_lsn` and log the skip line. With a PG
  primary the goopg standby never catches up; all\-goopg replays the data but logs nothing.
