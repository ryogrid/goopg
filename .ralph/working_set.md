Task: M0123-S4 sub-slice 32 — float4-common (no float8) CASE mix

Files:
  - internal/pgnodes/resolver_expr.go: selectCaseCommonType returns OidFloat4; coerceCaseResult gains int4/int8/numeric→float4 arms; wrapToFloat4Cast added
  - internal/pgnodes/rebuild.go: isImplicitToFloat4Cast added + wired into rebuildFuncExprWith
  - internal/pgnodes/case_test.go: 3 new PG18.3 goldens + degrade case float4_common_no_float8 removed
  - docs/design/0123-0005-pgnodes-bool-null-scalar.md: Deferred section updated
  - .ralph/fix_plan.md: sub-slice 32 LANDED, REMAINING updated

Key symbols: selectCaseCommonType, coerceCaseResult, wrapToFloat4Cast, isImplicitToFloat4Cast

Hypothesis/Findings:
  - Previously: selectCaseCommonType returned (0,false) for float4-common CASE → SQL text degrade.
  - Fix: return (OidFloat4,true); coerceCaseResult wraps int/numeric→float4 via implicit casts
    (float4(int4)=318 / float4(int8)=652 / float4(numeric)=1745, all funcformat 2).
  - No outer float8(float4) column cast needed — PG 18.3 stores casetype 700 directly.
  - Per-result float4() conv calls carry funcformat 0 and round-trip through catalog.RegprocName
    (not caught by isImplicitToFloat4Cast which checks funcformat==2).
  - All 31 CASE tests PASS (goldens + codec + rebuild round-trip + degrade).
  - All pgnodes + executor + full build PASS.

Next step: operator-driven view-qual coercion (next in REMAINING list).

Gates run: UNITS PASS (pgnodes + executor + full pre-commit), build OK, ralph-state-guard REPAIRED+OK.

In-flight: none
