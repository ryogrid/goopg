(idle — nothing in flight)

Last loop (#30): M0119-0004 **FK `MATCH FULL` round-trip in pg_dump**
(DU-002 slice 309) — LANDED. Design `0119-0004-fk-match-full-roundtrip.md`.

PG's pg_get_constraintdef_worker (ruleutils.c) emits ` MATCH FULL` BETWEEN the
`REFERENCES …(…)` list and the `ON UPDATE`/`ON DELETE` clauses for
confmatchtype='f'. goopg surfaced no match type at all (MATCH never in FK
grammar; confmatchtype hardcoded 's'; deparse never emitted clause) → MATCH FULL
silently degraded to MATCH SIMPLE on restore.

Threaded `MatchFull bool` end-to-end (mirrors NotValid/Deferrable):
- parser ddl.go: new `parseFKMatchType` helper (accepts MATCH FULL|PARTIAL|
  SIMPLE; only FULL→true), wired into ALL THREE FK forms (inline column
  REFERENCES, table-level FOREIGN KEY parseTableForeignKey, ALTER TABLE ADD FK).
- ast.go: ColumnDef.FKMatchFull, TableForeignKeyDef.MatchFull, AlterTableAction.MatchFull.
- catalog.go: ForeignKey.MatchFull + pg_constraint builder row[14]='f'/'s'.
- executor operators_ddl.go: all 3 catalog.ForeignKey build sites set MatchFull.
- executor expr.go buildForeignKeyDefString: ` MATCH FULL` before ON UPDATE/DELETE.

Gates: new DU-002 slice 309 in TestPort_PgDumpConnectionSetup (mf_child_fk →
`MATCH FULL;` in pg_dump stdout) PASS; new unit TestForeignKeyMatchFullRoundTrip
PASS; parser+catalog+executor suites PASS; `go build ./...` clean; pgbench smoke
= pre-commit hook.

NEXT loop — remaining open under M0119-0004 (probe TestPort_PgDumpConnectionSetup
for the next getter-battery gap):
- pg_dump 002–010 catalog-view parity battery (further slices). Probe for the
  next pg_get_*def / catalog-projection gap (e.g. exclusion-constraint operators,
  GENERATED column expressions, collation/opclass on index columns, comment
  round-trip via pg_description).
- extended-protocol commit-time deferral (architecturally entangled — extended
  protocol is auto-commit-per-statement; see memory).
Other M0119: M0119-0002 (CLOG store swap Part B — highest blast radius,
dedicated full-gate) / M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck).
