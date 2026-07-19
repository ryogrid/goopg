(idle — nothing in flight)

Last loop (#35): M0123-S4 sub-slice 21 — explicit float-family `::type` casts
(float4/float8). LANDED + committed. Extends sub-slices 19/20's funcformat-1
machinery across the binary-float boundary: all six TYPCATEGORY_NUMERIC types
(int2/int4/int8/numeric/float4/float8) have a pg_cast conv func, so any `expr::T`
between them is a COERCE_EXPLICIT_CAST (funcformat 1) FuncExpr kept in adbin.
`5::float4`/`5::float8`/`5.5::float4`/`5.5::float8`/`9999999999::float4`/`::float8`
+ nested `(5.5::float8)::int4` now emit canonical pg_attrdef.adbin (was SQL text).

Files: internal/pgnodes/resolver_expr.go (isNumericFamilyType accepts float4/float8;
numericFamilyCastFuncid full float matrix — int→float 236/318/652/235/316/482,
numeric↔float 1745/1746/1742/1743, float↔float 311/312, float→int
238/319/653/237/317/483); rebuild.go (explicitCastTypeName float arms, funcformat==1
guard separates the implicit CASE→float8 casts 311/316/482/1746);
internal/pgnodes/cast_test.go (+7 goldens, degrade swap text→float8);
internal/testport/oracle_pgnodes_adbin_test.go (+7, now 49). NO new node/codec.

Gates GREEN: full pgnodes pkg; cast_test 7 new goldens (golden/codec/rebuild loops);
adbin oracle 49/49 byte-identical vs LIVE PG18.3 (1.52s); executor/initdb attrdef
siblings green; TestE2E_FailoverGoopgToPG (6.66s async+sync); build/vet/gofmt clean;
pgbench smoke via pre-commit.

Next (M0123-S4 REMAINING): (1) float4-common (no float8) CASE result mix — needs
int/numeric→float4 arms + outer float8(float4) column cast (selectCaseCommonType/
coerceCaseResult); (2) date-time-family CASE coercion — needs a `date` OID-1082
datum (datum.go) + `::date`/`::timestamptz` string-literal fold in a natural/non-
column context; (3) typmod-qualified numeric/float target (`::numeric(10,2)` =
numeric(numeric,int4) length coercion); (4) operator-driven view-qual coercion
(unblocks int2/timestamptz literals); (5) a float literal leaf in parser + float
Const datum would unlock top-level float cast sources (nested-only today).
