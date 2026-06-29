(idle — nothing in flight)

Last loop (#39): M0119-0004 **ALTER STATISTICS … SET STATISTICS round-trip in
pg_dump** (DU-002 slice 317) — LANDED. Design
`0119-0004-alter-statistics-set-statistics.md`.

goopg had NO `ALTER STATISTICS` statement (parse error) and the pg_statistic_ext
virtual row hardcoded stxstattarget=-1, so a per-object statistics target could
never be recorded and pg_dump never re-emitted the ALTER. Fix threads it
end-to-end:
- parser: new AlterStatisticsStmt (Name/IfExists/Target/HasTarget); ALTER
  STATISTICS branch in parseAlter (IF EXISTS, schema-qualified name, SET
  STATISTICS n incl. leading `-` for -1 reset; RENAME/OWNER/SET SCHEMA = no-ops).
- catalog: StatisticsObject.StatTarget *int + lock-safe SetStatisticsTarget;
  pg_statistic_ext virtual row projects the value (else default -1).
- executor: execAlterStatistics — n>=0 → &n, -1 → nil.
- planner: route AlterStatisticsStmt to DDL; ddlTag → "ALTER STATISTICS" (+ also
  added "CREATE STATISTICS", was falling through to "OK").
GOTCHA: default stays -1 NOT true int-NULL — TypedVirtualCell parses "" int4 cell
as StringConst"" → pg_dump atoi("")=0 → spurious SET STATISTICS 0. -1 is
byte-identical to NULL for pg_dump (getExtendedStatistics maps both to -1).

Gates: DU-002 slice 317 in TestPort_PgDumpConnectionSetup PASS vs real pg_dump
18.3 (4.5s); TestParseAlterStatisticsSetStatistics; TestSetStatisticsTargetProjection;
parser/catalog/planner/executor/server suites PASS; go build clean; pgbench smoke
= pre-commit hook.

NEXT loop — next pg_dump getter-battery gap (M0110-0001 / DU-002). Candidates:
- ALTER STATISTICS … OWNER TO / RENAME TO / SET SCHEMA (currently parsed as
  no-ops; pg_dump emits OWNER via getRoleName, not asserted yet).
- Other M0119: M0119-0002 (CLOG store swap Part B) / M0119-0005 (pg_waldump) /
  M0119-0006 (pg_amcheck). Extended-protocol commit-time deferral is
  architecturally entangled (see memory goopg_extended_protocol_autocommit).
