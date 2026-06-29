(idle — nothing in flight)

Last loop (#35): M0119-0004 **CREATE STATISTICS round-trip in pg_dump**
(DU-002 slice 314) — LANDED. Design `0119-0004-create-statistics-roundtrip.md`.

pg_dump's dumpStatisticsExt selects pg_get_statisticsobjdef(oid); goopg's parser
discarded the (kinds) clause + ON column list, StatisticsObject carried neither,
and pg_get_statisticsobjdef was unimplemented (NULL) → statistics object silently
dropped from dump. Threaded Kinds/Columns/HasExpr through:
- parser ast.go/ddl.go: CreateStatisticsStmt.{Kinds,Columns,HasExpr};
  parseCreateStatisticsTail captures (kinds) idents + ON simple-column list;
  expression target sets HasExpr + skip. Fixed latent IF NOT EXISTS bug
  (acceptIdentKeyword("if") never matched keyword token → KwIf/KwNot/KwExists).
- catalog.go: StatisticsObject.{Kinds,Columns,HasExpr} + RegisterStatisticsFull;
  StatisticsByOID; BuildStatisticsObjDef (mirrors ruleutils.c
  pg_get_statisticsobj_worker — kinds clause suppressed when all 3 enabled or
  single-col; schema-qualified FROM via LookupTableByOID; "" for expr-bearing).
- executor operators_ddl.go: execCreateStatistics → RegisterStatisticsFull.
- executor expr.go: new pg_get_statisticsobjdef(oid) builtin.

Dump-fidelity only. Expression statistics flagged + omitted (deparser follow-up).

Gates: DU-002 slice 314 in TestPort_PgDumpConnectionSetup PASS vs real pg_dump
18.3 (4.7s); new units TestBuildStatisticsObjDef + TestParseCreateStatistics PASS;
parser+catalog+executor suites PASS; `go build ./...` clean; pgbench smoke =
pre-commit hook.

NEXT loop — next pg_dump getter-battery gap. Candidates: expression extended
statistics (CREATE STATISTICS ON (a+b) — needs AST deparser; HasExpr already
flags it); per-column COLLATE on an index EXPRESSION (verify parseExpr COLLATE
postfix path vs ColCollations). Other M0119: M0119-0002 (CLOG store swap Part B)
/ M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck). Extended-protocol commit-time
deferral is architecturally entangled.
