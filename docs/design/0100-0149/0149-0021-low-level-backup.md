# 0149-0021 — Port recovery/t/042\_low\_level\_backup.pl

Status: done \(2026\-11\-25\). `TestPort_Recovery042LowLevelBackupFull`
\(`internal/testport/recovery_042_low_level_backup_test.go`\) ports 042 in three pairings, all passing and ungated:
all\-goopg; a PG 18.3 primary with goopg recovery nodes; a goopg primary with PG 18.3 recovery nodes.

042 takes a backup with pg\_backup\_start, a file copy and pg\_backup\_stop. It copies pg\_control last, after a
checkpoint that follows the creation of a `canary` table. Without backup\_label the copy recovers from pg\_control's
later checkpoint and the canary is missing. With backup\_label, recovery starts at the label's redo and the canary is
there. The gaps:

- **pg\_backup\_stop\(\) in FROM**: `select labelfile from pg_backup_stop()` failed; the function's OUT columns
  \(lsn, labelfile, spcmapfile\) were unknown to both the analyzer and the planner. Both now have them; the planner
  arm \(`planRecordFuncScan`\) is a one\-call ScalarFuncScan whose composite result the operator splits into columns.
  spcmapfile is an empty string, as in PG, not NULL.
- **do\_pg\_backup\_stop on a primary**: goopg only switched segments. It now writes XLOG\_BACKUP\_END \(main data =
  the start point\) and takes its end as the stop LSN. It then switches segments and writes the backup history file
  `<segment>.<offset>.backup` into pg\_wal, marked `.ready`. With archiving on, it waits until the stop segment and
  the history file are archived \(`XLogArchiveIsBusy`\), with PG's NOTICE and WARNING texts. Without the record, PG
  recovering a goopg backup fails with "WAL ends before end of online backup".
- **read\_backup\_label**: goopg archive recovery ignored backup\_label. `ReadBackupLabel` parses START WAL LOCATION,
  CHECKPOINT LOCATION, START TIMELINE and BACKUP FROM; an unparsable label is the "invalid data" FATAL. Archive
  recovery takes its redo, checkpoint and timeline from the label and starts at the redo's segment. It logs "starting
  backup recovery with redo LSN …, checkpoint LSN …, on timeline ID …". After recovery the label is renamed to
  backup\_label.old.
- **An empty pg\_wal**: the WAL writer started at 0 while the restored WAL begins at the label's segment;
  `Writer.StartAt` moves an empty writer to that segment before the restored bytes are adopted.
- **Commit status**: initdb.Open sweeps xids below pg\_control's nextXid that have no outcome to aborted. A
  low\-level backup's pg\_control is later than the WAL being replayed, so the canary's transaction was swept.
  Archive recovery now stamps each replayed commit and abort, including subtransactions, into the live transaction
  manager \(`ArchiveReplayHooks.Xact`\), as the standby replayer does.
- **Catalog**: relations created after the backup started exist only in the replayed catalog heaps. Archive recovery
  now refreshes every database's catalog after replay \(`RefreshAllCatalogsAfterReplay`\).

Not done:

- backup\_label in crash recovery \(no recovery.signal\) and on a standby \(standby.signal\): only archive recovery
  reads it;
- PG renames backup\_label as recovery starts and copies the label's checkpoint into pg\_control; goopg renames
  it after recovery and keeps pg\_control's checkpoint fields;
- backupEndRequired / backupStartPoint in pg\_control: goopg does not refuse to finish recovery before XLOG\_BACKUP\_END;
- tablespace\_map is always empty, CleanupBackupHistory does not remove older history files, and a standby's
  pg\_backup\_stop writes no history file \(PG: minRecoveryPoint as the stop point\).
