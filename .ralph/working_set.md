Task: M0123-S4 sub-slice 12 — CASE **simple form** (`CASE operand WHEN val …`)
→ canonical CASEEXPR with a CaseTestExpr placeholder. COMPLETE (committing).

Landed: `CASE operand WHEN val THEN … END` now resolves canonically (was SQL
text), reproducing transformCaseExpr (parse_expr.c): operand → CaseExpr.arg,
each `WHEN val` → OpExpr `placeholder = val` whose left arg is a CaseTestExpr
typed from the operand (typeId/typeMod/collation). Deparse inverse (ruleutils)
shows only the OpExpr RHS.
- NEW NODE ir.go CaseTestExpr{Typeid,Typemod,Collation} + nodeTag CASETESTEXPR;
  outfuncs.go outCaseTestExpr (`{CASETESTEXPR :typeId N :typeMod N :collation N}`,
  matches generated _outCaseTestExpr); readfuncs.go readCaseTestExpr + dispatch.
- resolver_expr.go: resolveCaseExprWith handles e.Operand!=nil (Arg + testExpr);
  operandTypmodCollid extracts typmod/collid from Var/Const operand; new
  resolveCaseWhenCond builds each arm (searched=bool cond; simple=buildOpExpr
  placeholder=val). buildOpExpr needs an EXACT (opType=valType) `=` operator so
  the placeholder is never coercion-wrapped.
- rebuild.go: rebuildCaseExprWith restores Operand for Arg!=nil; new
  rebuildCaseWhenCond unwraps each OpExpr and rebuilds only Args[1] (WHEN value).
- Tests: case_test.go 4 live PG18.3 scalar adbin goldens (simple_int_else,
  _two_when_no_else, _numeric_else, _two_when_else); view_bool_null_test.go v13
  (`CASE client WHEN 5 THEN true ELSE false END`, Var-operand) golden + structural
  assert; degrade test now uses simple-form mixed-result.

Gates (GREEN): full pgnodes pkg, go build ./..., go vet ./internal/pgnodes/,
gofmt clean on touched files, executor TestCanonicalAttrdef/TestDefault,
ralph-state-guard (auto-repaired→consistent). pgbench smoke via pre-commit hook.

Key symbols: CaseTestExpr, resolveCaseExprWith, resolveCaseWhenCond,
operandTypmodCollid, rebuildCaseExprWith, rebuildCaseWhenCond, buildOpExpr.

Next step (sub-slice 13): select_common_type CROSS-TYPE result coercion (a CASE
whose WHEN/ELSE results differ in type — searched OR simple; defresult is "most
significant" per transformCaseExpr). Then the byte-diff oracle harness. All open
in M0123-S4 (design 0123-0005 §Deferred). Resume: resolver_expr.go
resolveCaseExprWith result-type seeding loop.

Gates run: pgnodes (green), executor attrdef/default (green), build/vet (green),
gofmt (clean), ralph-state-guard (repaired→consistent).
In-flight: none.
