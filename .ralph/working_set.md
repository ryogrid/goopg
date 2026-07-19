Task: M0123-S4 sub-slice 15 — CASE cross-FAMILY float coercion (float4→float8).
COMPLETE this loop; committing + pushing.

Landed: a CASE mixing float4+float8 results now folds to canonical casetype
float8 (was SQL text). Completes the binary-float family.
- selectCaseCommonType (resolver_expr.go): restructured to classify results into
  two DISJOINT families and fold only a within-family mix — exact-integer/numeric
  {int4,int8,numeric} (widest) OR float {float4,float8}→float8 (float8 IS
  preferred). A cross-family span (int4+float8) still degrades to SQL text.
- coerceCaseResult: new float4→float8 arm via wrapFloat4ToFloat8Cast = implicit
  float8(float4) cast FuncExpr (funcid 311, funcresulttype 701, funcformat 2),
  from pg_cast.dat (castsource float4→float8, castcontext 'i', method 'f' /
  prosrc ftod). Byte-identical to PG18.3.
- rebuild.go isImplicitFloat4ToFloat8Cast unwraps it in rebuildFuncExprWith →
  fixed point.
- datum.go: added OidFloat4=700 + float4/float8 caseTypeMeta (float8 byval).
NOTE: float CASE results are produced by float4()/float8() conv funcs (funcid
318/316, funcformat 0) — the pgnodes resolver has no float literal/::cast leaf.

Key symbols: selectCaseCommonType, coerceCaseResult, wrapFloat4ToFloat8Cast,
isImplicitFloat4ToFloat8Cast, rebuildFuncExprWith, caseTypeMeta.

Gates (GREEN): full pgnodes pkg, go build ./..., go vet ./internal/pgnodes/,
gofmt clean (4 files), executor TestCanonicalAttrdefText/TestDefault/
TestResolveForColumn + initdb TestRebuildAttrdefExpr, ralph-state-guard
(auto-repaired→consistent). pgbench smoke runs in pre-commit hook.
Goldens captured live from a throwaway PG18.3 (table cf, funcid 311/318/316).

Next step: pick from M0123-S4 REMAINING (fix_plan ~L1180): (a) UNIFIED cross-
family CASE coercion — generalize selectCaseCommonType to PG's per-typcategory
select_common_type walk (int/numeric+float → float8) + add int→float8/
numeric→float8 cast arms to coerceCaseResult (funcids: float8(int4)=316,
float8(int8)=482, float8(numeric)=1746); OR (b) the byte-diff oracle harness (the
last big M0123-S4 deliverable, goopg adbin/ev_action == real-PG18, :location
normalized). Recommend (b) the oracle gate. Resume file for (a):
internal/pgnodes/resolver_expr.go selectCaseCommonType/coerceCaseResult.

Gates run: pgnodes (green), executor/initdb siblings (green), build/vet (green),
gofmt (clean), ralph-state-guard (repaired→consistent), pgbench smoke (pre-commit).
In-flight: none.
