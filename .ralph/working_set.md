(idle — nothing in flight)

Last landed: DU-002 slice 181 (loop #149) — boolean-test predicate column DEFAULTs
(`*IsNullExpr`, `*IsBoolExpr`, `*IsDistinctFromExpr`) now round-trip through pg_dump;
sibling-path divergence with the executor twin closed. This CLOSES the column-DEFAULT
fall-through-corruption audit for every realistic `parser.Expr` node kind.

`DEFAULT (1 IS NOT NULL)` / `DEFAULT (true IS NOT TRUE)` / `DEFAULT (1 IS DISTINCT FROM 2)`
on boolean columns parse to `*IsNullExpr`/`*IsBoolExpr`/`*IsDistinctFromExpr`.
validateDefaultExpr accepts them (rejects only column refs / subqueries / aggregate-or-SRF;
every other node → return nil), so they reach pg_attrdef.adbin. Neither
catalog.formatExprForAttrdef nor executor.defaultExprToSQL had arms → both fell through to
fmt.Sprintf("%v", e) (Go pointer string), corrupting the dump. Fix: both twins render the
`IS [NOT] NULL` / `IS [NOT] TRUE|FALSE|UNKNOWN` / `IS [NOT] DISTINCT FROM` deparse PG's
pg_get_expr produces for NullTest/BooleanTest/DistinctExpr. Display-only; can't share code
(catalog below executor in import graph). Verified end-to-end: e2e test PASSES.

Files: internal/catalog/catalog.go (+3 cases), internal/executor/operators_ddl.go (+3 cases,
twin), internal/catalog/catalog_test.go (8 cases), internal/testport/pgdump_connsetup_test.go
(defcol gains nflag/bflag/dflag + 3 paren-robust assertions),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 181 section).
Gates: gofmt OK; go vet clean; catalog renderer test PASS; executor DDL/default tests PASS;
TestPort_PgDumpConnectionSetup PASS (3.04s, not skipped); pgbench pre-commit smoke on commit.

Next (slice 182 candidates): the column-DEFAULT fall-through audit is CLOSED for realistic
kinds. Remaining Expr kinds are contrived: *CollateExpr (collation-name quoting nuance) and
*InExpr (drags in validateDefaultExpr's non-recursion-into-InExpr validation gap + subquery
form). HIGHER value: (1) column STORAGE/COMPRESSION dump fidelity (real pg_dump feature; needs
parser keywords for CREATE TABLE column STORAGE/COMPRESSION + ALTER COLUMN SET STORAGE/
COMPRESSION); (2) deferred MINVALUE/MAXVALUE keyword-AST-node slice (HIGHER RISK: partition
routing); (3) close validateDefaultExpr array/row/CASE/InExpr recursion gap (executor semantic
change — needs its own gates). Strongly consider pivoting OFF micro-DEFAULT slices to (1).
