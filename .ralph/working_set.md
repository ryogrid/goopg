Task: M0123-S4 sub-slice 6 — route BOOLEANTEST through the VIEW-query dispatch.
COMPLETE this loop (committing).

Landed: a view `WHERE (x) IS [NOT] TRUE/FALSE/UNKNOWN` now emits canonical
pg_rewrite.ev_action (was SQL-text fallback). Two dispatch arms only, reusing the
sub-slice-5 recursion-injectable `*With` BOOLEANTEST builders:
- resolver_query.go: queryScope.resolveExpr adds
  `case *parser.IsBoolExpr: resolveBooleanTestWith(v, s.resolveExpr)` (after the
  IsNullExpr arm; Var-aware operand).
- rebuild_query.go: viewRebuildScope.rebuildExpr adds
  `case *BooleanTest: rebuildBooleanTestWith(v, s.rebuildExpr)` (mirrors NullTest).
- view_bool_null_test.go (NEW goldens): v5 `(client>0) IS TRUE` (booltesttype 0)
  + v6 `(client>0) IS NOT FALSE` (booltesttype 3) — live PG18.3 ev_action, joined
  into the table-driven forward / RoundTrip / RebuildViewQuery-fixed-point tests.

No new IR/codec/builder. executor sys_pg_rewrite.go (ResolveViewQuery) picks it
up automatically.

Gates (GREEN): pgnodes package (ViewQueryBoolNull + BooleanTest + full),
executor Rewrite/View/CanonicalAttrdef, go vet ./internal/pgnodes/,
go build ./..., gofmt -l clean. pgbench smoke via pre-commit hook.
Design 0123-0005 §"Sub-slice 6" + fix_plan S4 note + ledger row appended.

Key symbols: queryScope.resolveExpr, viewRebuildScope.rebuildExpr,
resolveBooleanTestWith, rebuildBooleanTestWith.

Next step (next loop): CaseExpr (CASE WHEN) — full codec+resolver+rebuild+scalar
+view with live PG18.3 adbin/ev_action goldens (ir.go/outfuncs.go/readfuncs.go/
resolver_expr.go/rebuild.go, mirror the BOOLEANTEST slice shape), then
DistinctExpr (IS DISTINCT FROM), then the byte-diff oracle harness. Optionally add
a BOOLEANTEST standby pg_get_viewdef parse case to e2e_failover_goopg_to_pg_test.go.

In-flight: none.
