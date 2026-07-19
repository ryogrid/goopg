(idle — nothing in flight)

Last loop (#33): M0123-S4 sub-slice 20 — explicit numeric↔integer `::type` cast
(extends sub-slice 19's funcformat-1 machinery across the int/numeric boundary).
LANDED + committed. `5.5::int4`/`::int8`/`::int2`, `(-2.5)::int4`, `5::numeric`,
`9999999999::numeric` now emit canonical pg_attrdef.adbin (was SQL text). PG stores
each as a COERCE_EXPLICIT_CAST (funcformat 1) FuncExpr: numeric_int4=1744 /
numeric_int8=1779 / numeric_int2=1783 (numeric→int); int4_numeric=1740 /
int8_numeric=1781 (int→numeric); operand resolved at NATURAL type first.
resolver_expr.go isIntegerType→isNumericFamilyType + integerCastFuncid→
numericFamilyCastFuncid (6 cross-boundary arms). rebuild.go
explicitIntegerCastTypeName→explicitCastTypeName (numeric arms; funcformat==1 guard
still separates the implicit 1740/1781 funcformat-2 unwrap). rebuildConst numeric
arm handles the negative fold (fixed point).

Gates GREEN: internal/pgnodes/cast_test.go (6 new live PG18.3 goldens + degrade
matrix now numeric→float8) via golden/codec/rebuild loops; adbin oracle now 42/42 vs
PG18.3 (1.45s); TestE2E_FailoverGoopgToPG (6.3s, one transient FAIL from a scratch-
cluster collision — re-ran clean) + initdb/executor attrdef siblings green;
build/vet/gofmt clean; state-guard reconciled; pgbench smoke via pre-commit.

Next (M0123-S4 REMAINING): (1) float-family explicit-cast arms — float↔{int,numeric}
(`5.5::float8`=float8 conv FuncExpr, `5::float4`) in resolveCastExpr/
numericFamilyCastFuncid; (2) a `date` OID-1082 datum in datum.go + `::date`/
`::timestamptz` string-literal fold for the date-time-family CASE arms; (3)
typmod-qualified numeric target (`::numeric(10,2)` = numeric(numeric,int4) length
coercion); (4) float4-common (no float8) CASE mix; (5) operator-driven view-qual
coercion. Resume: internal/pgnodes/resolver_expr.go resolveCastExpr/
numericFamilyCastFuncid (add float arms) + datum.go (date datum).
