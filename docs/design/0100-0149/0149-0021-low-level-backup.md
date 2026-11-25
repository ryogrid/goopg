# 0149-0021 — Port recovery/t/042\_low\_level\_backup.pl

Status: done \(2026\-11\-25\). `TestPort_Recovery042LowLevelBackupFull`
\(`internal/testport/recovery_042_low_level_backup_test.go`\) passes on all three pairings, ungated: all\-goopg, a PG
18.3 primary with goopg recovery nodes, and a goopg primary with PG 18.3 recovery nodes. As in Cluster.pm, the
node restored with backup\_label is a standby \(init\_from\_backup has\_restoring defaults to `standby => 1`\). An
earlier version of the port used recovery.signal and was reopened; see "Standby from backup\_label" below.

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

## Standby from backup\_label \(M0149\-0021a\)

- **Start position**: at standby start, `startStandbyFromBackupLabel` \(cmd/goopg/standby.go\) reads backup\_label and
  logs "starting backup recovery with redo LSN …". With an empty pg\_wal it moves the WAL writer to the label's redo
  segment \(`Writer.StartAt`; pg\_control's checkpoint redo without a label\). The archive feeder and the continuous
  replayer both start from the writer's position, and they started at 0. The label is then renamed to
  backup\_label.old.
- **Readiness**: PG's postmaster writes "ready" into postmaster.pid's status line only when hot standby begins, at
  consistency \("database system is ready to accept read\-only connections"\). pg\_ctl start \-w waits for it. goopg
  counted as ready once it listened, so a test's first query could race replay and get "the database system is not
  yet accepting connections". The control socket now answers `PMSTATUS`: "starting" until a hot standby is
  consistent, "standby" with hot\_standby = off, else "ready". The test cluster's Start waits past "starting".

Not done:

- the standby's consistency point is still pg\_control's checkpoint redo, not the backup end \(XLOG\_BACKUP\_END /
  backupEndRequired\), so a low\-level backup's standby becomes consistent later than PG's would; goopg's own
  postmaster.pid has no status line \(PMSTATUS is answered over the control socket\);
- backup\_label in crash recovery \(no recovery.signal\) and on a standby \(standby.signal\): only archive recovery
  reads it;
- PG renames backup\_label as recovery starts and copies the label's checkpoint into pg\_control; goopg renames
  it after recovery and keeps pg\_control's checkpoint fields;
- backupEndRequired / backupStartPoint in pg\_control: goopg does not refuse to finish recovery before XLOG\_BACKUP\_END;
- tablespace\_map is always empty, CleanupBackupHistory does not remove older history files, and a standby's
  pg\_backup\_stop writes no history file \(PG: minRecoveryPoint as the stop point\).
