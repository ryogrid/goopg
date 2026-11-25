# 0149-0023 — Port recovery/t/003\_recovery\_targets.pl

Status: done \(2026\-11\-25\). `TestPort_Recovery003RecoveryTargetsFull`
\(`internal/testport/recovery_003_recovery_targets_test.go`\) passes on all three pairings and is ungated:
all\-goopg, a PG 18.3 primary with goopg standbys, and a goopg primary with PG 18.3 standbys.

003 bumps the primary's xid epoch with pg\_resetwal, takes a backup, and writes five batches of rows. The batches
are marked by an xid, a time, a restore point and an LSN. Standbys restored from the backup \(Cluster.pm
has\_restoring: `standby => 1`\) must stop at each target and pause there, serving reads. Two targets set at once
are refused at startup. Archive recovery \(`standby => 0`\) to a restore point that does not exist must FATAL.

## 0023a — name, time and immediate targets

- **pg\_create\_restore\_point\(text\)** writes XLOG\_RESTORE\_POINT, whose main data is xl\_restore\_point: rp\_time
  TimestampTz and a 64\-byte NUL\-padded name. It logs `restore point "…" created at X/X` and returns the record's
  end. Its errors are PG's: "recovery is in progress", "WAL level not sufficient for creating a restore point", and
  "value too long for restore point \(maximum 63 characters\)". Replay treats the record as a no\-op, as before.
- **GUCs**: recovery\_target accepts only "immediate". recovery\_target\_name must be shorter than MAXFNAMELEN.
  recovery\_target\_time's hook rejects now, today, tomorrow and yesterday; the value is parsed as timestamptz in
  the server's TimeZone when recovery starts \(validateRecoveryParameters\).
- **Multiple targets**: PG raises "multiple recovery targets specified" from the target GUCs' assign hooks while
  the configuration loads, so any server with two targets fails to start. goopg counts all five settings and checks
  at startup for every server. An emptied setting does not count, so 003's "name then '' then time" is allowed.
- **Archive recovery** \(`RunArchiveRecoveryWith`\) stops:
  - at a restore point with the target name, always after it;
  - at a transaction end by time: inclusive stops after a commit or abort at exactly the target and before a later
    one; exclusive stops before one at or past the target;
  - for 'immediate', after the first record that reaches consistency \(pg\_control's minimum recovery point or
    backup end, the label's start, never before the redo\).
- StopReason and StopMessage carry PG's texts: `at restore point "…"`, `before|after <time>`,
  `reached consistency`; `recovery stopping at restore point "…", time …`, `… after reaching consistency`. A target
  that was never reached is the FATAL "recovery ended before configured recovery target was reached". That FATAL
  is now logged at FATAL in the server log, not only on stderr.

## 0023c — SRF in a single INSERT VALUES row

The all\-goopg standbys counted 1005 rows, but the fault was on the primary.
`INSERT INTO tab_int VALUES (generate_series(1001,2000))` inserted one row.
transformInsertStmt treats a single VALUES row as a target list, so a set\-returning function there expands to rows
\(in lockstep, NULL\-padded, as in a SELECT list\). A multi\-row VALUES list is a VALUES RTE, where
transformValuesClause refuses it: "set\-returning functions are not allowed in VALUES". `analyzeInsert` now plans a
single row holding a set\-returning function, and no DEFAULT, as INSERT … SELECT <row>. It raises the error for
multi\-row lists. The standalone VALUES form still lacks the check; it is filed as M0149\-0095.

## 0023d — BASE\_BACKUP's end of backup

A PG 18.3 standby of a goopg primary with recovery\_target = 'immediate' never became ready. PG's cassert build
asserted in FinishWalRecovery: it was consistent before reading any record \("completed backup recovery … end LSN" =
the redo\), stopped "after reaching consistency", and had no last record. The cause was a shortcut in BASE\_BACKUP:
its shipped pg\_control carried minRecoveryPoint = 1 and backupEndPoint = redo, and no XLOG\_BACKUP\_END was written.
BASE\_BACKUP now runs do\_pg\_backup\_stop's primary arm after the data files and before any fetched WAL, as
perform\_base\_backup does. It writes XLOG\_BACKUP\_END, whose end is the stop LSN, switches segment, and writes the
backup history file \(.ready\). The shipped image leaves minRecoveryPoint and backupEndPoint unset, like the live
file basebackup.c sends. The restoring server raises minRecoveryPoint to the redo itself and, from the label's
"BACKUP METHOD: streamed", waits for XLOG\_BACKUP\_END. A goopg standby's consistency point is unchanged \(the
redo\).

## 0023b — recovery targets on a standby

The per\-record decision is one function, `EvaluateRecoveryStop` \(recoveryStopsBefore and recoveryStopsAfter\),
shared by archive recovery and the standby's continuous replay. The standby's StreamReplayer takes a
`ReplayTarget` before it starts. It stops before a record or after it as the target says; 'immediate' stops after
the record that makes the standby consistent. It logs PG's "recovery stopping …" line, and then acts on
recovery\_target\_action:

- pause: it waits at the end of recovery with "pausing at the end of recovery" and HINT "Execute
  pg\_wal\_replay\_resume\(\) to promote.". Hot\-standby reads are served and pg\_get\_wal\_replay\_pause\_state
  reports paused. pg\_wal\_replay\_resume\(\) ends recovery and promotes.
- promote: it promotes at once.
- shutdown: it stops the server; this goes through the immediate\-shutdown path, not PG's clean shutdown.

Not done:

- pause at the target during start\-time archive recovery \(M0149\-0022b\);
- the standby's 'shutdown' action is an immediate shutdown;
- time targets ignore COMMIT PREPARED / ABORT PREPARED.
