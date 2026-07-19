Task: M0123-S4 sub-slice 16 — UNIFIED cross-FAMILY CASE coercion (any
int/numeric/float → float8). COMPLETE this loop; committing + pushing.

Landed: a CASE mix spanning the exact-integer/numeric and binary-float families
that CONTAINS float8 now folds to canonical casetype float8 (was SQL text).
- selectCaseCommonType (resolver_expr.go): rewritten from two disjoint families
  to ONE walk over PG's numeric type category {int4,int8,numeric,float4,float8};
  float8 is the category's PREFERRED type so it wins whenever present (precedence
  float8>numeric>int8>int4). A float4-but-no-float8 mix → common type float4 +
  outer column cast (unmodeled → degrade).
- coerceCaseResult: new int4/int8/numeric→float8 arms via wrapToFloat8Cast
  (float8(int4)=316 / float8(int8)=482 / float8(numeric)=1746, funcformat 2,
  castcontext 'i'). float4→float8 still via wrapFloat4ToFloat8Cast (311).
- rebuild.go isImplicitToFloat8Cast unwraps {316,482,1746} funcformat==2 (guard
  load-bearing: same OIDs appear funcformat 0 for explicit float8(int) calls).

Key symbols: selectCaseCommonType, coerceCaseResult, wrapToFloat8Cast,
isImplicitToFloat8Cast, rebuildFuncExprWith.

Gates (GREEN): full pgnodes pkg, go build ./..., go vet ./internal/pgnodes/,
gofmt clean (3 files), executor TestCanonicalAttrdefText/TestDefault/
TestResolveForColumn + initdb TestRebuildAttrdefExpr/TestRebuildViewFromEvAction,
ralph-state-guard. pgbench smoke runs in pre-commit hook. Goldens captured live
from a throwaway PG18.3 (tables ucf/ucf5, funcids 316/482/1746/311/318).

Next step: pick from M0123-S4 REMAINING (fix_plan ~L1194): (a) float4-common
(no float8) CASE mix — needs int/numeric→float4 cast arms + model the OUTER
float8(float4) column cast that wraps a sub-column-type CASE (resolveCaseExprWith
/ ResolveForColumn); OR (b) the byte-diff oracle harness (goopg adbin/ev_action
== real-PG18 for identical DDL, :location normalized) — the last big S4
deliverable. Recommend (b).

Gates run: pgnodes (green), executor/initdb siblings (green), build/vet (green),
gofmt (clean), ralph-state-guard (pending final run), pgbench smoke (pre-commit).
In-flight: none.
