# 0149-0023 — Port recovery/t/003\_recovery\_targets.pl

Status: in progress. 0023a and 0023c done \(2026\-11\-25\); 0023b and 0023d open. `TestPort_Recovery003RecoveryTargetsFull`
\(`internal/testport/recovery_003_recovery_targets_test.go`\) is gated by `GOOPG_TAP_003`. It has three pairings:
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

## Open

- 0023b: a goopg standby ignores every recovery target; it must stop at the target and pause there.
- 0023d: a PG standby of the goopg primary with recovery\_target = 'immediate' never becomes ready.
