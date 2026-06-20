Task: DU-002 slice 299 — COMPLETE, committing + pushing.

Last landed: oracle-verified fixture for the nested-arithmetic EXPRESSION-INDEX-COLUMN
deparse context — the second context `executor.defaultExprToSQL` feeds (the index-key
expression in `catalog.Index.ColExprStrings[i]`, vs slice 298's index PREDICATE in
`PredicateString`). FIXTURE-ONLY: slice 298's BinaryOp parenthesization already produces
correct bytes here, so this locks it in vs real pg_dump 18.3 (no production code change).

`CREATE INDEX foo_calc_expr_idx ON public.foo (((qty + id) * mgr_id))` dumps as
`USING btree ((((qty + id) * mgr_id)))` — FOUR nested parens:
  inner `(qty+id)` + `*` wrap (both from defaultExprToSQL) + per-column `(%s)` +
  `USING btree (…)` column-list parens (both from catalog.BuildIndexDef).
Verified `pg_get_indexdef` uses prettyFlags=PRETTYFLAG_INDENT (no PAREN) and
`pg_get_expr(indexprs)` == goopg's defaultExprToSQL output (`((qty + id) * mgr_id)`).
(NOTE: easy to mis-count — I first predicted 3 parens by forgetting the column-list wrapper.)

Files:
- internal/testport/pgdump_connsetup_test.go — NEW foo_calc_expr_idx index DDL (~L2430);
  NEW assertion in indexDefs list (~L6478); NEW negative guard for corrupt `(qty + id * mgr_id)`
  (after the slice-298 predicate guard, ~L6525).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 299 section (paren-nesting table).
- .ralph/fix_plan.md (loop #67 progress); .ralph/deferral_ledger.md (slice 299 landed, 300 deferred).

Key symbols: defaultExprToSQL + binaryOpSymbolForDefault (executor/operators_ddl.go),
BuildIndexDef (catalog/catalog.go:6795, expression-column branch ~L6824-6836 wraps `(exprStr)`),
idx.ColExprStrings populated at operators_ddl.go:6117.

Gates: gofmt clean; go vet clean; TestPort_PgDumpConnectionSetup PASS (4.0s vs real pg_dump 18.3);
make ralph-state-guard; pgbench pre-commit smoke (enforced by .githooks/pre-commit).

Next (slice 300+): add an oracle-verified fixture for ONE remaining unfixtured defaultExprToSQL
context — partition-key expr `PARTITION BY RANGE ((((a+b)*c)))` OR func-arg default `((1+2)*3)`
with a binary op (renderer already parenthesizes; no byte-verified fixture yet); OR a multi-column
/ NULL-typed DEFAULT variant on the partition-leaf ALTER path.
