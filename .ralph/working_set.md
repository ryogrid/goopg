Task: M0123-S4 sub-slice 9 — canonical DISTINCTEXPR scalar node. COMPLETE (committing).

Landed: a `bool DEFAULT (a IS [NOT] DISTINCT FROM b)` now emits canonical PG18
DISTINCTEXPR adbin (was SQL-text fallback). make_distinct_op re-tags a `=` OpExpr
as T_DistinctExpr (same struct), so:
- ir.go: `type DistinctExpr OpExpr` + nodeTag "DISTINCTEXPR".
- outfuncs.go/readfuncs.go: factored shared outOpExprFields/readOpExprFields;
  outDistinctExpr/readDistinctExpr reuse them (byte-identical to OPEXPR, token only).
- resolver_expr.go: `*parser.IsDistinctFromExpr`→resolveDistinctFrom(+…With);
  buildDistinctExpr reuses buildOpExpr("="); NOT form wraps in NOT BoolExpr.
- rebuild.go: `*DistinctExpr`→rebuildDistinctExpr(+…With); NOT wrapper rebuilds via
  existing rebuildBoolExpr NOT arm → `NOT (a IS DISTINCT FROM b)` (fixed point).
- distinct_test.go: 5 live PG18.3 adbin goldens (int opno96 / NOT-BOOLEXPR wrapper /
  text opno98 inputcollid100 / numeric opno1752 / bool opno91).

Gates (GREEN): pgnodes full pkg, go vet ./internal/pgnodes/, go build ./...,
executor TestDefault*/TestCanonicalAttrdef* siblings. gofmt clean on touched files
(resolver_expr_test.go/timestamptz_test.go flags are pre-existing go-version skew).
pgbench smoke via pre-commit hook (on commit).

Key symbols: DistinctExpr, outOpExprFields, readOpExprFields, resolveDistinctFrom(With),
buildDistinctExpr, rebuildDistinctExpr(With).

Next step (sub-slice 10): DISTINCTEXPR VIEW-query wiring — route
resolver_query.go queryScope.resolveExpr `*parser.IsDistinctFromExpr`→
resolveDistinctFromWith(v, s.resolveExpr) + rebuild_query.go viewRebuildScope.rebuildExpr
`*DistinctExpr`→rebuildDistinctExprWith (mirror sub-slice 6/8, 2 arms only). View
ev_action golden ALREADY captured: vdist `x IS DISTINCT FROM y` →
`:quals {DISTINCTEXPR :opno 96 :opfuncid 65 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({VAR :varno 1 :varattno 1 ...} {VAR :varno 1 :varattno 2 ...}) :location -1}`.
Add to view_bool_null_test.go + a b5c_view4 standby pg_get_viewdef assert. Then
`IS DISTINCT FROM NULL`→NullTest special case, then CASE simple form.

Gates run: pgnodes (green), executor default/attrdef (green), go build/vet (green).
In-flight: none.
