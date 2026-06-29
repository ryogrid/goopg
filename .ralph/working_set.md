(idle — nothing in flight)

Last loop (#31): M0119-0004 **partial EXCLUDE constraint `WHERE` predicate
round-trip in pg_dump** (DU-002 slice 310) — LANDED. Design
`0119-0004-partial-exclude-where-roundtrip.md`.

`parseExcludeConstraint` (parser/ddl.go) consumed `USING method (col WITH op)` +
optional `INCLUDE (cols)` but NEVER a trailing `WHERE`, so a partial exclusion
constraint silently lost its predicate → degraded into an all-rows exclusion on
restore. PG renders EXCLUDE via pg_get_indexdef_worker which appends ` WHERE (%s)`
(ruleutils.c:1564) AFTER the operator/INCLUDE list and BEFORE the DEFERRABLE tail.

Threaded the predicate end-to-end reusing partial-index plumbing:
- ast.go: new `TableConstraintDef.ExclusionWhere Expr`.
- parser ddl.go parseExcludeConstraint: parse optional `WHERE` via p.parseExpr().
- executor operators_ddl.go: new `applyExclusionPredicate(idx, pred)` sets
  idx.HasPredicate/Predicate/PredicateString=defaultExprToSQL(pred); wired into
  all 3 EXCLUDE build sites (named btree-=, anon btree-=, createExclusionIndexStub).
- executor expr.go buildConstraintDefString EXCLUDE branch: append ` WHERE `+
  PredicateString before DEFERRABLE.

Gates: DU-002 slice 310 in TestPort_PgDumpConnectionSetup (pex_excl → `EXCLUDE
USING btree (a WITH =) WHERE (b > 0);`) PASS vs real pg_dump 18.3; new unit
TestBuildConstraintDefExclusionWhere + integration
TestExclusionConstraintPartialWhereRoundTrip PASS; executor+parser+catalog suites
PASS; `go build ./...` clean; pgbench smoke = pre-commit hook.

NEXT loop — remaining open under M0119-0004 (probe TestPort_PgDumpConnectionSetup
for the next getter-battery gap): pg_dump 002–010 catalog-view parity battery
(further slices) — candidates: FK `ON DELETE SET NULL (col_list)` (PG15
confdelsetcols), collation/opclass on index columns, comment round-trip via
pg_description on more object types. Extended-protocol commit-time deferral is
architecturally entangled (auto-commit-per-statement; see memory). Other M0119:
M0119-0002 (CLOG store swap Part B — full-gate) / M0119-0005 (pg_waldump) /
M0119-0006 (pg_amcheck).
