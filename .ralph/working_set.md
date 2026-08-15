# Working set — M0134-0002 alter_table.sql (C2 grammar cluster, slice 5 landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. C2 = "ALTER-TABLE
grammar cluster". This loop landed **C2 slice 5 — ALTER COLUMN TYPE … USING**.

**Status:** C2 slice 5 COMPLETE + committed (`fec178bd`) + pushed. C2 remains OPEN.

**Findings:** `ALTER COLUMN c TYPE <type> USING <expr>` now works with PG-exact
semantics and NO data loss. Parser: new `AlterTableAction.UsingExpr` field +
`p.parseExpr()` after `parseColumnType()` in the `parseAlter` TYPE arm (ddl.go:8947),
mirroring SET DEFAULT (ddl.go:8875). Planner: exported
`planner.ResolveAlterColumnTypeUsing` (planner.go:528) wrapping `resolveExpr` +
`singleBindingContext` — resolves ColumnRefs against the OLD column positions.
Executor: `execAlterColumnType` (operators_ddl.go:21438) resolves the USING expr
before any mutation, evaluates it per-row (`evalExpr`, expr.go:330), coerces via
`evalCast`, and **propagates** a PG-exact 42804 error (two variants + hints,
tablecmds.c:14495-14511) BEFORE the Phase-3 truncation — a failed rewrite leaves
the table intact (the C10 data-loss root). Also fixed a slot RUnlock/Unpin leak on
the new error path. Closes the 11 `syntax error at or near (got using)` sites
(`got using` → 0 in the diff).

**Files:** internal/parser/{ast.go,ddl.go,alter_test.go}, internal/planner/planner.go,
internal/executor/{operators_ddl.go,operators_ddl_alter_type_using_test.go},
docs/design/0134-0002-alter-table-sql-divergence.md, .ralph/deferral_ledger.md
(3 new rows), .ralph/fix_plan.md (C2 fifth-slice bullet).

**Key symbols:** `execAlterColumnType` (operators_ddl.go:21438), per-row hook
:21535-21568 (`convErr` deferred past RUnlock/Unpin); `ResolveAlterColumnTypeUsing`
(planner.go:528); `AlterTableAction.UsingExpr` (ast.go:3289).

**Deferred this loop (ledger, 3 rows):** evalCast permissive coercion set
(int→bool, int8→int4 narrowing — the `anothertab` cascade); whole-row / generated-
column / `SET DATA TYPE` edge rejections; DEFAULT revalidation + typmod/format_type_be.

**Remaining C2 sub-gaps (ranked by error-site count ÷ risk):** comma multi-action
(7, structural — root cause is branch-and-`return` in `parseAlterTableAction`
ddl.go:9053, arms return before the comma loop), RENAME `<col>` TO bare (3),
ANALYZE tab(col) (4, re-route — an ANALYZE/VACUUM gap), NOT VALID (2), STORAGE (2),
OF/NOT OF (3), DROP COLUMN IF EXISTS (1, one-line `acceptKeyword(KwIf)`),
DROP CONSTRAINT IF EXISTS (1), SET WITHOUT OIDS (1), ENFORCED dup (1, C9-masked).

**Next step:** C2 slice 6 = **comma multi-action** — research-first (delegate to
`researcher`): map how `parseAlter`/`parseAlterTableAction` currently handle a
comma-separated action list (`ALTER TABLE t A, B, C`), which arms return before
reaching the comma loop, and what PG's `alter_table_cmds` grammar (gram.y) + the
existing goopg comma-handling (if any) look like. The 7 sites are `ALTER TABLE t
ALTER COLUMN a TYPE x, ALTER COLUMN b SET ...`-style. Design note → brief →
implement. If the structural refactor is too large, fall back to the one-liners
(DROP COLUMN IF EXISTS / DROP CONSTRAINT IF EXISTS) as a smaller checkpoint.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/parser|planner|executor/ -p 4` PASS; `scripts/pg-regress-runner.sh alter_table` — `got using` → 0 (overall diff still FAILs on pre-existing gaps); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); pre-commit pgbench smoke PASS (hook).

**Delegation:** researcher `0134-0002-c2-typeusing-research` DONE (2 rounds);
implementer `0134-0002-c2-typeusing-impl` DONE (one round); tester (gates) DONE.

**In-flight:** none.
