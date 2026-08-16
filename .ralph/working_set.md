# Working set — M0134-0002 alter_table.sql (C11a LANDED, committed 603a5e47)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop researched
C11 (the last named class), found it was three unrelated problems under one
design-doc cell, and landed the cheap isolated one. Diff **4073 → 4048 (−25)**.

**Landed (603a5e47):** ALTER TABLE structural actions on a **view** now raise
42809 `ALTER action %s cannot be performed on relation %q` + DETAIL
`This operation is not supported for views.` (`ATSimplePermissions`
tablecmds.c:6739 + `errdetail_relkind_not_supported` pg_class.c:24-37). New
`viewAllowedAlterAction` (explicit allow-list) + `alterActionName` (all 43
action kinds) + an **all-actions pre-scan** before the dispatch loop (mirrors
`ATPrepCmd` → atomic failure on multi-action ALTER). `Pos: 0` is load-bearing —
`ATSimplePermissions` never calls `errposition()`, so PG's .out has no `LINE 1:`
cursor (act.Pos() cost 10 diff lines).

**Files:** internal/executor/operators_ddl.go,
internal/executor/operators_ddl_c11a_view_guard_test.go (new),
docs/design/0134-0002-alter-table-sql-divergence.md (C11 row rewritten + new
"C11 decomposition + C11a" §), docs/design/README.md, .ralph/fix_plan.md,
.ralph/deferral_ledger.md (2 new rows: C11b, C11c).

**Key symbols:** `viewAllowedAlterAction`, `alterActionName`, `execAlterTable`
(guard sits after the pending-detach check, before the action loop),
`tbl.View != nil` (matview-exclusive — `execCreateMatView` never sets it).

**Findings (corrects the old design doc):** `internal/executor/view_dml.go`
DOES NOT EXIST (view DML is `internal/optimizer/view_dml.go`). "CREATE OR
REPLACE VIEW not propagating to dependents" is NOT the bug — dependent column
counts already track (slice-1 `viewColumnMap` fix); what diverges is the **View
definition: SQL text**, because goopg has **no deparser** (`execCreateView`
stores `RawDef` verbatim; `expr.go:8242-8292` echoes it). That is the
"top-level-`*` freeze" = C11c.

**Next step (NEXT LOOP — re-read the fix_plan banner first):** no named
correctness class remains for alter_table.sql. Cheapest remaining work, in
order: (a) the ledgered **C9 residuals** — already-a-partition 42809 re-ATTACH
guard (alter_table.sql:2697), ADD CONSTRAINT duplicate-name merge accounting,
ONLY-guards for SET NOT NULL / ADD CONSTRAINT; (b) the formatter tail
C7/C12/C13/C14 (message text, NOTICE/IF EXISTS, EXPLAIN verbosity) — measure
which owns the most of the 4048 lines before picking. **C11b** (`to_json`
family) and **C11c** (ruleutils deparser) are DEFERRED — C11c deserves its own
milestone and would also close the ledgered CHECK-constraint rendering gap.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/executor/`
PASS; `go test -run TestAlterTableOnViewRelkindGuard -v` PASS (8 subtests,
coordinator re-verified pre-commit); `go test ./internal/optimizer/` PASS;
`scripts/pg-regress-runner.sh alter_table` 4048 (from 4073); create_view (2505)
and updatable_views (4156) byte-identical to a stash-verified baseline;
pre-commit pgbench smoke PASS. tpch-spotcheck NOT run — DDL-guard-only diff, no
query/planner/codec path.

**Nightly triage:** ci/logs/action-items.md run 20260816-005117 — all 3 `## AI-`
items already filed under M-NIGHTLY (001 open, 002/003 ticked). Nothing new.

**Delegation:** researcher brief tmp/ralph-handoffs/0134-0002-c11-research/
(DONE, NEEDS-DECISION → coordinator split C11); implementer brief
tmp/ralph-handoffs/0134-0002-c11a-view-guard/ (DONE, 1 round).

**In-flight:** none.
