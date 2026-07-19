Task: M0123-S4 sub-slice 10 — DISTINCTEXPR VIEW-query wiring. COMPLETE (committing).

Landed: a view `WHERE a IS [NOT] DISTINCT FROM b` over base-relation columns now
serializes to canonical PG18.3 pg_rewrite.ev_action (was SQL-text fallback). Pure
two dispatch arms — the `…With` recursion-injectable builders already existed:
- resolver_query.go: queryScope.resolveExpr `*parser.IsDistinctFromExpr` →
  resolveDistinctFromWith(v, s.resolveExpr). NOT form's NOT-BOOLEXPR wrapper is
  emitted inside resolveDistinctFromWith, so no extra arm.
- rebuild_query.go: viewRebuildScope.rebuildExpr `*DistinctExpr` →
  rebuildDistinctExprWith(v, s.rebuildExpr). NOT wrapper rebuilds via existing
  rebuildBoolExprWith NOT arm (re-enters DISTINCTEXPR arm).
- view_bool_null_test.go: v9 (`client IS DISTINCT FROM 5` DISTINCTEXPR opno 96
  VAR+CONST) + v10 (`client IS NOT DISTINCT FROM 5` NOT-BOOLEXPR wrapper), both
  LIVE-captured from PG18.3 (fresh cluster, bench_log relid 16384). Added to
  viewBoolNullCases (forward/round-trip/rebuild-fixed-point) + structural asserts.

Gates (GREEN): pgnodes full pkg, go vet ./internal/pgnodes/, go build ./..., gofmt
clean on touched files. pgbench smoke via pre-commit hook (on commit).

Key symbols: resolveDistinctFromWith, rebuildDistinctExprWith, DistinctExpr,
queryScope.resolveExpr, viewRebuildScope.rebuildExpr.

Next step (sub-slice 11): `IS DISTINCT FROM NULL` special case — PG's
make_nulltest_from_distinct rewrites `x IS DISTINCT FROM NULL` → `x IS NOT NULL`
(NULLTEST, not DISTINCTEXPR); currently a NULL operand degrades to SQL text (see
design doc 0123-0005 Deferred + resolver_expr.go resolveDistinctFromWith). Then
the CASE simple form (`CASE operand WHEN …` — CaseTestExpr placeholder) +
select_common_type cross-type result coercion (both still open in M0123-S4).

Gates run: pgnodes (green), go build/vet (green), gofmt (clean), ralph-state-guard (OK).
In-flight: none.
