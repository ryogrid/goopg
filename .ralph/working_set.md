Task: M0123-S4 sub-slice 7 — canonical CASEEXPR/CASEWHEN (searched form),
scalar column-DEFAULT scope. COMPLETE this loop (committing).

Landed: a column DEFAULT `CASE WHEN cond THEN result … [ELSE result] END`
(searched form) now emits canonical PG18 pg_attrdef.adbin (was SQL-text
fallback). Codec+resolver+rebuild in one commit (sibling rule):
- ir.go: CaseExpr{Casetype,Casecollid,Arg,Args,Defresult,Location} + CaseWhen
  {Expr,Result,Location}; Args is []Node of *CaseWhen.
- outfuncs.go/readfuncs.go: outNode/readNode CASEEXPR+CASEWHEN arms +
  out/readCaseExpr, out/readCaseWhen.
- resolver_expr.go: `*parser.CaseExpr`→resolveCaseExpr + resolveCaseExprWith(rec)
  (recursion-injectable, ready for view path). Searched form only; WHEN conds→
  bool; all results+ELSE same non-collatable casetype (casecollid 0); omitted
  ELSE → typed NULL Const (newNullConst).
- datum.go: caseTypeMeta allowlist (bool/int2/int4/int8/oid/numeric/timestamptz)
  + newNullConst.
- rebuild.go: `*CaseExpr`→rebuildCaseExpr + rebuildCaseExprWith(rec); NULL
  defresult ↔ omitted ELSE (fixed point).
- NEW gate case_test.go: 5 live PG18.3 adbin goldens + degradation matrix.
- Reconciled executor sys_pg_attrdef_test.go TestCanonicalAttrdefText
  (case-expr/case-no-else flipped SQL-text→canonical; case-mixed stays text).

Gates (GREEN): pgnodes full package, go vet ./internal/pgnodes/, go build ./...,
gofmt -l clean, executor TestCanonicalAttrdefText, internal/initdb full.
pgbench smoke via pre-commit hook (on commit).

Key symbols: resolveCaseExprWith, rebuildCaseExprWith, caseTypeMeta,
newNullConst, outCaseExpr/outCaseWhen, readCaseExpr/readCaseWhen.

Note: `postgres` symlink (→ ../postgres) went missing at session start; restored
it this loop (untracked convenience symlink, not committed).

Next step (next loop): CASE view-query wiring (sub-slice 8) — route
resolver_query.go queryScope.resolveExpr `*parser.CaseExpr`→
resolveCaseExprWith(v, s.resolveExpr) + rebuild_query.go viewRebuildScope.
rebuildExpr `*CaseExpr`→rebuildCaseExprWith(v, s.rebuildExpr) (mirror sub-slice
6), with a live PG18.3 ev_action view golden. Then the simple form (CaseTestExpr
node + operand=val expansion), then DistinctExpr (IS DISTINCT FROM), then the
byte-diff oracle harness.

In-flight: none.
