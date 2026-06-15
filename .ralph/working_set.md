Task: M0110-0003 (AC-002 amcheck) — loop #4. LANDED gap #7a (database-name
pattern resolution via row-valued IS [NOT] NULL semantics). Remaining: gap #7c
(per-database amcheck-installed detection), then clog XidStatusFunc wiring, then
AC-002…AC-005 TAP port + CSV flip.

=== WHAT LANDED (this loop) — committed on align-data-structure-with-pg ===
gap #7a: pg_amcheck `--database <pat>` now errors `no connectable databases to
check matching "<pat>"` for an unresolvable name. Root cause was EXECUTOR-side,
not amcheck-specific: `compile_database_list`'s bootstrap query uses
`COUNT(*) FILTER (WHERE d IS NOT NULL)` where `d` is a whole-row ref to a
LEFT-OUTER-JOINed CTE (planner expands whole-row → RowExpr). The IsNullExpr case
evaluated the RowExpr to a composite Datum and called Datum.IsNull() — a
constructed RowExpr is never a NULL Datum, so `d IS NOT NULL` was wrongly true
for an outer-join non-match → every pattern looked checkable. Fix: SQL/PG
row-null semantics for a RowExpr operand of IS [NOT] NULL
(executor.evalRowNullTest: IS NULL true iff EVERY field null, IS NOT NULL iff
EVERY field non-null; recursive on nested rows; NOT inverses).

Files: internal/executor/expr.go (IsNullExpr RowExpr branch ~line 681 +
evalRowNullTest helper near evalMergeWholeRow), internal/executor/
row_null_test_test.go (NEW: TestRowValuedNullTest truth table),
internal/testport/pgamcheck002_port_test.go (gap #7a now regression-guard
t.Fatalf at patProbe; self-skip re-keyed to gap #7c via `pg_amcheck template1`),
docs/design/0110-0008-amcheck-sql-surface-plan.md.

Key symbols: evalRowNullTest, IsNullExpr case in evalExprSlot, planner.RowExpr
(whole-row var expansion at planner.go:9342), pg_amcheck compile_database_list
include_pat CTE, amcheck_sql (pg_extension lookup).

Gates run: go test ./internal/executor ./internal/planner ./internal/analyzer
./internal/server PASS; TestRowValuedNullTest PASS; TestPort_PgAmcheck002Nonesuch
SKIPs cleanly on gap #7c (was SKIP on 7a); gofmt+vet clean; build ./... OK;
make ralph-state-guard OK. No TPC-H spotcheck — IS-NULL change is scoped to
RowExpr operands only (scalar IS NULL path untouched), no executor row-count path
changed.

=== NEXT STEP (resume point) — AC-002 gap #7c ===
gap #7c: per-database amcheck-installed detection. pg_amcheck runs `amcheck_sql`
(`SELECT n.nspname, x.extversion FROM pg_extension x JOIN pg_namespace n ON
x.extnamespace=n.oid WHERE x.extname='amcheck'`) in each connectable DB and warns
`skipping database "template1": amcheck is not installed` when it returns 0 rows.
goopg's pg_extension is a single GLOBAL catalog, so amcheck CREATE EXTENSIONd in
`postgres` ALSO shows in template1/template0 → warning never fires, template1 is
checked instead of skipped (exit 0, empty stderr). NEEDS per-database
extension-catalog isolation so pg_extension/pg_namespace amcheck rows are visible
only in the DB where CREATE EXTENSION ran. This is a distinct, larger feature.
After gap #7c: clog XidStatusFunc tier wiring, then AC-002…AC-005 TAP port + CSV
flip (docs/test-port/postgres-oracle-port-status.csv).

=== CONTEXT ===
Main tree is clean — commit engine work directly on align-data-structure-with-pg.
.ralph/fix_plan.md is churned by the driver — progress recorded here + ledger.
