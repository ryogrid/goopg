# 0149-0020 — Port recovery/t/024\_archive\_recovery.pl

Status: done \(2026\-11\-25\). `TestPort_Recovery024ArchiveRecoveryFull`
\(`internal/testport/recovery_024_archive_recovery_test.go`\) ports 024 in three pairings, all passing and ungated:
all\-goopg; a PG 18.3 origin with goopg recovery nodes; a goopg origin with PG 18.3 recovery nodes.

024 takes a backup, restarts the origin with wal\_level = minimal and then back with replica, and archives the WAL in
between. Archive recovery from the backup, and a standby restoring from the archive, must both stop with
FATAL `WAL was generated with "wal_level=minimal", cannot continue recovering`. Four pieces were missing:

- **XLogReportParameters**: a goopg primary wrote no XLOG\_PARAMETER\_CHANGE record at startup. It now compares the
  eight settings with pg\_control after recovery \(`reportParameters`, `cmd/goopg/main.go`\). When they differ it
  writes the record \(`EncodeParameterChangePG`, the 28\-byte xl\_parameter\_change\) if wal\_level changed or is at
  least replica, flushes it, and updates pg\_control. The checkpointer's settings echo follows the running values
  \(`Checkpointer.SetGUCParams`\) instead of initdb's.
- **The FATAL in xlog\_redo**: replaying a PARAMETER\_CHANGE with wal\_level = minimal while archive recovery was
  requested \(standby.signal or recovery.signal present at start — `SetArchiveRecoveryRequested`\) returns a
  `RecoveryFatalError` with PG's message, detail and hint. Crash recovery replays the record as before.
- **Reporting it**: archive recovery and the standby replayer log the error at FATAL \(`LogLevelFatal`, slog's
  ERROR\+4\) and the server shuts down immediately. The tapnode log renders `level=ERROR+4` as PG's `FATAL:`.
- **Archive recovery's start point**: `RunArchiveRecoveryWith` replayed each segment from the last checkpoint record
  in it. For 024 that skipped the PARAMETER\_CHANGE between the backup's redo and the next shutdown checkpoint. It now
  starts at pg\_control's checkPointCopy.redo, as InitWalRecovery does.

The tapnode harness gained `StartExpectExit`, which waits for the server process itself to exit \(the pid file is
removed first\), so nothing writes into the data dir while the test cleans up.

Not done:

- XLogReportParameters at the end of a standby's recovery \(promotion\);
- a FATAL shutdown exits with status 0, not the postmaster's failure status;
- the checkpointer still writes its settings echo at every checkpoint, which PG does not.
