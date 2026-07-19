Task: M0123-S4 sub-slice 8 — CASE view-query wiring. COMPLETE this loop (committing).

Landed: a view `WHERE CASE WHEN … THEN … [ELSE …] END` (searched form) now emits
canonical PG18 pg_rewrite.ev_action (was SQL-text fallback). Two dispatch arms only
(no new IR/codec), mirroring sub-slice 6:
- resolver_query.go: queryScope.resolveExpr `*parser.CaseExpr`→resolveCaseExprWith(v, s.resolveExpr).
- rebuild_query.go: viewRebuildScope.rebuildExpr `*CaseExpr`→rebuildCaseExprWith(v, s.rebuildExpr).
  (searched-form / same-casetype / caseTypeMeta guards live inside the *With builders.)
- view_bool_null_test.go: +2 live PG18.3 ev_action goldens (v7 one-WHEN+ELSE bool;
  v8 two-WHENs+omitted-ELSE→typed-NULL defresult constisnull=t) into the 4 table-driven
  tests; +v7/v8 structural asserts in TestViewQueryBoolNullStructure.
- e2e_failover_goopg_to_pg_test.go: +b5c_view3 CASE view — real PG18 standby reports
  relhasrules=true + pg_get_viewdef PARSES the CASE ev_action.

Gates (GREEN): pgnodes full pkg, go vet ./internal/pgnodes/ + ./internal/testport/,
go build ./..., gofmt -l clean, TestE2E_FailoverGoopgToPG (6.3s). pgbench smoke via
pre-commit hook (on commit).

Key symbols: resolveCaseExprWith, rebuildCaseExprWith (both already recursion-injectable
from sub-slice 7), queryScope.resolveExpr, viewRebuildScope.rebuildExpr.

Next step (next loop): CASE simple form (`CASE operand WHEN val …` — needs CaseTestExpr
placeholder + `operand = val` OpExpr per WHEN, mirrors transformCaseExpr) and
select_common_type cross-type result coercion. Then `IS DISTINCT FROM` (DistinctExpr),
then operator-driven view-qual implicit coercion, then the byte-diff oracle harness.

In-flight: none.
