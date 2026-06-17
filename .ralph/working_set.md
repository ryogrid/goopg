(idle — nothing in flight)

Last landed: DU-002 slice 173 (loop #140) — function-call column DEFAULT
(`DEFAULT now()`) round-trips through pg_dump. REAL DIVERGENCE FIXED.

Bug: `formatExprForAttrdef` (catalog.go — the producer of `pg_attrdef.adbin`,
which pg_dump reads back via pg_get_expr) handled ONLY literal constants; a
`*parser.FuncCall` fell through to `fmt.Sprintf("%v", e)`, printing a Go
pointer/struct string. So a `DEFAULT now()` column dumped a corrupt, restore-
breaking DEFAULT clause. Sibling-path bug: `executor.defaultExprToSQL` (the
proargdefaults renderer) already handled FuncCall, but the catalog twin on the
pg_dump path did not (can't share code — catalog is below executor in imports).

Fix: added a `*parser.FuncCall` case to `formatExprForAttrdef` mirroring
`defaultExprToSQL` — renders `[schema.]name(arg, …)`, recursing on args.
Display-only; routing + default evaluation untouched.

Files: internal/catalog/catalog.go (FuncCall case in formatExprForAttrdef),
internal/catalog/catalog_test.go (TestFormatExprForAttrdefFuncCall),
internal/testport/pgdump_connsetup_test.go (defcol fixture + 2 assertions),
docs/design/0110-0001-pg-dump-tap-port.md (slice 173), .ralph/fix_plan.md.
Gates: gofmt OK; go vet ./internal/testport/ clean; go build ./internal/... OK;
TestFormatExprForAttrdefFuncCall + full catalog suite PASS;
TestPort_PgDumpConnectionSetup PASS (2.72s, not skipped); pgbench pre-commit
smoke on commit (.githooks/pre-commit).

Next (slice 174 candidates): (1) CURRENT_TIMESTAMP keyword default — PG stores
it in adbin WITHOUT parens (`CURRENT_TIMESTAMP`), but goopg parses it as a
FuncCall{Name:"current_timestamp"} → would now render `current_timestamp()`
(parens added); needs a keyword-vs-call distinction. (2) function-call default
with literal args end-to-end (`DEFAULT lpad('x',5)` — unit-tested but no e2e).
(3) deferred MINVALUE/MAXVALUE keyword-AST-node (HIGHER RISK: partition routing).
(4) column STORAGE/COMPRESSION dump fidelity (needs parser keywords).
