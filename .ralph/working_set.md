# Working set — M0134-0002 alter_table.sql (C4 ADD-FK validation landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop landed
**C4 — ADD FOREIGN KEY validation semantics** (commit `0518b4a4`).

**Findings:** the ADD FK arm (`case parser.AlterTableAddForeignKey`,
operators_ddl.go:7731-7789) only checked referenced-table 42P01 then appended to
`tbl.ForeignKeys`. So alter_table.sql:355/358/361 (all `add constraint
attmpconstr foreign key ...`) silently succeeded where PG rejects (42703/42703/23503),
piling up four same-name entries that shadowed the later `VALIDATE CONSTRAINT`
scan (first stale `NotValid=false` match) — masking out:499-500. Fix adds, in PG
order: 42710 dup-name guard (cross-kind, explicit name only), 42703 source then
42703 ref column check (`fkColumnExists`, case-sensitive dropped-skipping), 23503
existing-row scan (`!NotValid`, reuse C3 `validateFKConstraintExistingRows`),
plus VALIDATE FK 23503 Pos-suppression (`ri_ReportViolation` no errposition).
Diff 4145→4113 (−32), FK block byte-green; 5 tests.

**Files:** internal/executor/operators_ddl.go (ADD-FK arm + 2 helpers +
VALIDATE Pos-suppression), internal/executor/operators_ddl_fk_add_validation_test.go
(5 tests); docs/design/0134-0002-alter-table-sql-divergence.md (§C4);
.ralph/deferral_ledger.md (row 1413 → resolved; NEW row for 42804/42830/42908/
0A000 residuals); fix_plan.md progress note.

**Key symbols:** `fkColumnExists`, `fkConstraintNameInUse` (new helpers),
`execAlterTableAddForeignKey` (ADD FK arm), `validateFKConstraintExistingRows`
(reused scan), `assertParentExists` (23503 source), VALIDATE arm `:7838`.

**Deferred (1 new ledger row):** 42804 FK type-compat (`findFkeyCast`
tablecmds.c:10435), 42830 no-unique-constraint (`transformFkeyCheckAttrs`
:13657), 42908 column-count, 0A000 system-column; + EqualFold-vs-strcmp in the
42710 guard (quoted mixed-case only).

**Next step:** C10 — ALTER TYPE data-loss (failed int8→int4 rewrite leaves the
table EMPTY, `internal error: expected int, got kind 1`; evalCast coercion
matrix, ledger row 1398). Alternatively C11 (rules-subsystem) or C9 residuals
(descendant-partition recursion, ONLY-on-partitioned DROP COLUMN). Remaining
alter_table work: C9 residuals, C10/C11.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/executor/`
PASS (cache warm); `scripts/pg-regress-runner.sh alter_table` 4145→4113 (−32),
FK block byte-green (verified vs HEAD worktree baseline); pre-commit pgbench
smoke PASS (12828 tps select-only, 0 failed). tpch-spotcheck NOT re-run —
DDL-only FK-path change, no query/planner/codec path touched.

**Delegation:** researcher `0134-0002-c4-fk-dup-name-research` DONE (root cause
(C): stale pile-up predates DROP; fix = 42710+42703+23503, not 42710 alone);
implementer `0134-0002-c4-fk-add-validation` DONE (diff −32).

**In-flight:** none.
