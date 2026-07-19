Task: M0123-S4 sub-slice 11 — `IS [NOT] DISTINCT FROM NULL` → NullTest rewrite. COMPLETE (committing).

Landed: an undecorated NULL literal on either side of `IS [NOT] DISTINCT FROM`
now resolves to a canonical NULLTEST (was SQL-text fallback), reproducing PG's
transformAExprDistinct → make_nulltest_from_distinct (parse_expr.c). Rewrite fires
pre-resolution; negation folded into nulltesttype (NO NOT wrapper). Both scalar
and view-query paths inherit it (shared resolveDistinctFromWith).
- resolver_expr.go: new helper distinctNullTestArg (bare *parser.NullConst on
  right-then-left, mirroring exprIsNullConstant order) + special case in
  resolveDistinctFromWith: IS DISTINCT FROM NULL→nulltesttype 1 (IS_NOT_NULL),
  IS NOT DISTINCT FROM NULL→nulltesttype 0 (IS_NULL). NULL::type cast → ordinary
  DISTINCTEXPR path (not a NullConst). Rebuild UNCHANGED (NullTest already
  round-trips to `x IS [NOT] NULL`, the pg_get_viewdef fixed point).
- view_bool_null_test.go: v11 (`client IS DISTINCT FROM NULL`, nulltesttype 1) +
  v12 (`client IS NOT DISTINCT FROM NULL`, nulltesttype 0, no NOT wrapper), both
  LIVE-captured from PG18.3 (fresh cluster, bench_log relid 16384). Added to
  viewBoolNullCases (forward/round-trip/rebuild-fixed-point) + structural asserts.

Gates (GREEN): pgnodes full pkg + verbose v11/v12 (forward/round-trip/structure),
go build ./..., go vet ./internal/pgnodes/, gofmt clean on touched files.
pgbench smoke via pre-commit hook (on commit).

Key symbols: distinctNullTestArg, resolveDistinctFromWith, NullTest, IsNull/IsNotNull.

Next step (sub-slice 12): CASE **simple form** (`CASE operand WHEN …` — CaseTestExpr
placeholder); resolveCaseExprWith currently returns ErrUnsupported for the simple
form (resolver_expr.go). Then select_common_type cross-type result coercion, then
the byte-diff oracle harness. All open in M0123-S4 (see design 0123-0005 Deferred).

Gates run: pgnodes (green), go build/vet (green), gofmt (clean), ralph-state-guard (pending).
In-flight: none.
