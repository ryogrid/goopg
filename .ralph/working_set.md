# Working set — M0134-0002 alter_table.sql (C9 slice 1 landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop landed
**C9 slice 1 — inherited-DDL guards** (commit `a5d06075`). After C2 (grammar
cluster) completed last loop, a researcher reassessment re-ran the regress and
ranked the remaining correctness classes; C9 inheritance (553 lines) is the
largest, and its guard half is bounded.

**Findings:** diff re-captured at HEAD `bdba00b1` = **4349 lines / 104 hunks /
746+/849−**. Remaining classes ranked: C9 553 · C6 356 · C7 179 · C10 146 ·
C7/C12 87 · C12 85 · C11 58 · C3 46 · C4 21 · C8 17 · C13 1, plus two NEW classes
surfaced: **PLPGSQL** 39 (`v := expr FROM table` assignment rejected — blocks all
6 `check_ddl_rewrite`) and **TYPEDS** 7 (`'epoch'` timestamp literal rejected).

**C9 slice 1 landed:** five ALTER-TABLE refusals — DROP/RENAME COLUMN, ADD COLUMN
ONLY, DROP/RENAME CONSTRAINT on inherited columns/constraints — byte-exact 42P16
messages from `ATExecDropColumn`/`renameatt_internal`/`ATExecAddColumn`/
`dropconstraint_internal`/`rename_constraint_internal`. `ONLY` guards key off new
`hasInheritanceChildren` (INHERITS∪PARTITION); inherited guards key off
`col.Inherited`/`nc.InhCount` with `colStillInherited`/`parentStillHasColumn`
live-hierarchy narrowing (stale-flag-safe) + a NO INHERIT flag-clear; parser
records `AlterTableStmt.Only`. Diff 4349→4298 (−51), zero new divergence; 8 tests.

**Files:** internal/parser/{ast.go,ddl.go}, internal/executor/operators_ddl.go,
internal/executor/operators_ddl_inherit_guards_test.go (new),
docs/design/0134-0002-alter-table-sql-divergence.md (§C9 first slice),
.ralph/fix_plan.md + .ralph/deferral_ledger.md (3 rows).

**Key symbols:** `hasInheritanceChildren`, `colStillInherited`,
`parentStillHasColumn` (operators_ddl.go), `AlterTableStmt.Only`,
`execAlterDropColumn`, `execAlterTableDropConstraint`, `execAlterTableAddColumn`.

**Deferred (3 ledger rows):** `Column.InhCount int` multi-parent bookkeeping
(attinhcount 1-vs-2 `c1` merge; bool guard can't fire on the merge case);
LIKE+ATTACH-PARTITION `Inherited`; INHERIT child-validation; INHERITS merge
NOTICEs; inline `CONSTRAINT con1` name (C7).

**Next step:** brief + implement the **C3 constraint-validation scans** slice
(ADD CHECK / SET NOT NULL / VALIDATE / ADD PK must scan existing rows and raise
23514/23502 — mirrors existing `validateFKConstraintExistingRows`
operators_ddl.go:10467; ~46 lines, bounded, the doc's next-unmasked class, and it
unmasks the sequential-apply gap). Alternative: continue C9 with the
`Column.InhCount int` follow-up. New classes PLPGSQL/TYPEDS get their own slices
after C3/C9.

**Gates run (this loop):** `scripts/pg-regress-runner.sh alter_table` (baseline
4349 → post 4298, −51, zero new divergence); `go build ./...` PASS; `go test
./internal/executor/ ./internal/parser/ ./internal/catalog/` PASS (8 new tests);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35); pre-commit pgbench smoke PASS
(12567 tps select-only); `make ralph-state-guard` (see status block).

**Delegation:** researcher `0134-0002-c3-reassess-research` DONE (report has the
per-class table + recommendation); implementer `0134-0002-c9-inherit-guards` DONE
(1 round); tester tpch-spotcheck DONE.

**In-flight:** none.
