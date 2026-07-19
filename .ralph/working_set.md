Task: M0123-S4 sub-slice 14 — CASE cross-FAMILY integer coercion (int4→int8).
COMPLETE this loop; committing + pushing.

Landed: a CASE mixing int4+int8 results with NO numeric now folds to canonical
casetype int8 (was SQL text). Completes the {int4,int8,numeric} family.
- selectCaseCommonType (resolver_expr.go): returns the WIDEST family member present
  (numeric>int8>int4). None is a preferred type (only float8 is, per pg_type
  typispreferred), so PG's select_common_type walk always widens int4→int8→numeric.
  Anything outside {int4,int8,numeric} → false → SQL text.
- coerceCaseResult: new int4→int8 arm via wrapInt4ToInt8Cast = implicit int8(int4)
  cast FuncExpr (funcid 481, funcresulttype 20, funcformat 2), from pg_cast.dat
  (castsource int4→int8, castcontext 'i', method 'f'). Byte-identical to PG18.3.
- rebuild.go isImplicitInt4ToInt8Cast unwraps it in rebuildFuncExprWith → fixed point.
- datum.go: added OidFloat8=701 (for the degrade boundary).

Key symbols: selectCaseCommonType, coerceCaseResult, wrapInt4ToInt8Cast,
isImplicitInt4ToInt8Cast, rebuildFuncExprWith.

Gates (GREEN): full pgnodes pkg, go build ./..., go vet ./internal/pgnodes/,
gofmt clean (4 files), executor TestCanonicalAttrdefText/TestDefault/
TestResolveForColumn + initdb TestRebuildAttrdefExpr, ralph-state-guard
(auto-repaired→consistent). pgbench smoke runs in pre-commit hook.
Goldens captured live from a throwaway PG18.3 (table cw, funcid 481 confirmed).

Next step: pick from M0123-S4 REMAINING (fix_plan ~L1173): (a) the byte-diff
oracle harness (goopg emitted adbin/ev_action == real-PG18 for identical DDL,
:location normalized) — the last big M0123-S4 deliverable; OR (b) FLOAT-family
CASE coercion (float4→float8 cast + a float branch in selectCaseCommonType/
coerceCaseResult, OIDs 700/701); OR (c) operator-driven view-qual coercion.
Recommend (a) the oracle gate. Resume file for (b):
internal/pgnodes/resolver_expr.go selectCaseCommonType/coerceCaseResult.

Gates run: pgnodes (green), executor/initdb siblings (green), build/vet (green),
gofmt (clean), ralph-state-guard (repaired→consistent), pgbench smoke (pre-commit).
In-flight: none.
