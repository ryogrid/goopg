(idle — nothing in flight)

Last loop (#32): M0123-S4 sub-slice 19 — explicit integer `::type` cast
(int2/int4/int8). LANDED + committed. PG stores `expr::inttype` as a
COERCE_EXPLICIT_CAST (funcformat 1) FuncExpr naming the pg_cast conversion func
(int2(int4)=314 / int8(int4)=481 / int4(int8)=480 / int2(int8)=714), kept verbatim
in adbin; a cast to the operand's own type is a no-op (bare Const). New
resolver_expr.go resolveCastExpr/isIntegerType/integerCastFuncid (operand resolved
at NATURAL type); operandTypmodCollid gains a *FuncExpr arm (typmod -1 / collid
funccollid) → closes the "explicit-cast operand simple CASE" item; rebuild.go
explicitIntegerCastTypeName rebuilds to a ::type CastExpr (funcformat==1 guard).

Gates GREEN: internal/pgnodes/cast_test.go (7 live PG18.3 goldens + degradation
matrix) via golden/codec/rebuild-fixed-point loops; adbin oracle now 36/36 vs
PG18.3 (1.4s); TestE2E_FailoverGoopgToPG (6.8s) + initdb/executor attrdef siblings
green; build/vet/gofmt clean; state-guard consistent; pgbench smoke via pre-commit.

Next (M0123-S4 REMAINING): (1) non-integer explicit-cast arms — numeric↔int
(`5.5::int4`=numeric_int4 1744), string-literal→date/timestamptz fold in a
natural/non-column context (needs a `date` OID-1082 datum in datum.go) for the
date-time-family CASE arms; (2) an OUTER implicit column coercion in
ResolveForColumn so a CASE/expr whose casetype is implicitly-castable to the column
type canonicalizes (e.g. int8 col DEFAULT (CASE 5::int8 WHEN 1 THEN 10 ELSE 20 END)
where the int4-casetype CASE gets an outer int8(int4) funcformat-2 wrap); (3)
float4-common (no float8) CASE mix; (4) operator-driven view-qual coercion.
Resume: internal/pgnodes/resolver_expr.go resolveCastExpr/integerCastFuncid +
ResolveForColumn (post-resolve outer-column-cast wrap); datum.go (date datum).
