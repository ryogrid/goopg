# Working set — M0134-0002 alter_table.sql (ALTER-TYPE partition-key guard landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop landed the
**ALTER-TYPE partition-key guard** — the sibling of the DROP COLUMN guard from
commit `1b8d3825`. Commit `3e81aef1`.

**Findings:** `execAlterColumnType` (operators_ddl.go:22137) now raises 42P16
`cannot alter column %q because it is part of the partition key of relation %q`
before any rewrite, walking `tbl.PartitionKey` (bare key) + `tbl.PartitionKeyExprs`
(via `partitionKeyExprUsesColumn`). Key difference from DROP COLUMN: PG's
`ATExecAlterColumnType` carries `parser_errposition(pstate, def->location)`
(tablecmds.c:14450) while `ATExecDropColumn` does not, so this raise uses
`Pos: act.Pos()` (not `Pos: 0`). That required threading `colPos` (the
column-name token location) into `AlterTableAction.pos` via `parseAlterColumnAction`
(internal/parser/ddl.go). The two 42804 coercion-failure arms (evaluation-time,
no source location) were corrected `Pos: act.Pos()`→`Pos: 0`. Diff 4153→4145 (−8);
errposition verified byte-exact via raw psql (no off-by-one).

**Files:** internal/executor/operators_ddl.go (guard + 42804 Pos-0),
internal/parser/ddl.go (colPos threading), operators_ddl_partition_key_test.go
(TestAlterTablePartitionKeyGuardAlterType, 4 subtests);
docs/design/0134-0002-alter-table-sql-divergence.md (§"partition-key DROP COLUMN
guard" sibling note now "landed"); .ralph/deferral_ledger.md (row 1418 → resolved;
NEW row for descendant-partition recursion); fix_plan.md progress note.

**Key symbols:** `execAlterColumnType`, `partitionKeyExprUsesColumn` (walker),
`parseAlterColumnAction` (colPos), `evalCast` (42804 arms).

**Deferred (1 new ledger row):** descendant-partition recursion — PG recurses into
descendant partitions on ALTER/DROP COLUMN of a partitioned parent, so
`ALTER TABLE ONLY list_parted2 DROP COLUMN b` reports `… of relation "part_5"`
(descendant key) where goopg reports 42703. Diff :3929 (DROP) + :3934 (ALTER TYPE),
pre-existing. Resume: recurse into descendants + per-descendant key guard.

**Next step:** C4 — the ADD-FK duplicate-name guard (ledger row 1413:
`execAlterTableAddForeignKey` ~:7718 add a dup-name guard so VALIDATE CONSTRAINT
finds the newest `NotValid` FK entry; unblocks the FK VALIDATE regress anchor
alter_table.sql:378 → out:499-500). Alternatively C10 (evalCast coercion matrix,
row 1398) or C11 (rules-subsystem, all ledgered). Remaining alter_table work:
C9 residuals, C4/C10/C11.

**Gates run (this loop):** `go build ./...` PASS; targeted tests 17/17 + 4/4 PASS;
`go test ./internal/executor/` PASS (cache warm); `scripts/pg-regress-runner.sh
alter_table` 4153→4145 (−8), slice lines dropped, errposition byte-matches PG;
pre-commit pgbench smoke PASS (12910 tps select-only, 0 failed). tpch-spotcheck
NOT re-run — DDL-only change, no query/planner/codec path touched (Q12=2/Q13=35
was confirmed last loop on this same branch).

**Delegation:** tester `0134-0002-altertype-partition-guard` gate-run DONE (report
returned as text; env blocked writing report.md to tmp).

**In-flight:** none.
