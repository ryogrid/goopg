(idle — nothing in flight)

Last loop (#38): M0119-0004 **Expression extended-statistics round-trip in
pg_dump** (DU-002 slice 316) — LANDED. Design
`0119-0004-expression-statistics-roundtrip.md`.

Slice 314 declined any `CREATE STATISTICS s ON (a + b) FROM t` (HasExpr set →
BuildStatisticsObjDef returned ""), so dumpStatisticsExt (emits
pg_get_statisticsobjdef(oid) verbatim) silently dropped the object on dump.
Fix threads expression targets end-to-end:
- parser: CreateStatisticsStmt.Exprs []Expr — non-simple-column ON target now
  parsed via p.parseExpr() (tolerant p.idx rewind + skip on parse error).
- executor execCreateStatistics: deparses each Expr via defaultExprToSQL (already
  parenthesizes binary ops, leaves bare function calls unwrapped — matches
  ruleutils.c looks_like_function) → catalog.StatisticsObject.Exprs []string.
- catalog BuildStatisticsObjDef: declines only when HasExpr && len(Exprs)==0;
  ncolumns = len(Columns)+len(Exprs); emits columns first then expressions.
No pg_statistic_ext view change (getExtendedStatistics never reads stxkeys/exprs).

Gates: TestPort_PgDumpConnectionSetup PASS (4.6s) vs real pg_dump 18.3 (slice 316
statext_expr / statext_mix); TestBuildStatisticsObjDef extended (6 new cases);
parser+catalog+executor suites PASS; go build clean; pgbench smoke = pre-commit.

NEXT loop — next pg_dump getter-battery gap. Candidates:
- ALTER STATISTICS … SET STATISTICS n (stxstattarget) — needs ALTER STATISTICS
  parser + catalog StatTarget field + pg_statistic_ext.stxstattarget projection;
  dumpStatisticsExt already emits the ALTER when stattarget >= 0.
- Other M0119: M0119-0002 (CLOG store swap Part B) / M0119-0005 (pg_waldump) /
  M0119-0006 (pg_amcheck). Extended-protocol commit-time deferral is
  architecturally entangled (see memory goopg_extended_protocol_autocommit).
