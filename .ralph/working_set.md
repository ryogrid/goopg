(idle — nothing in flight)

Last loop (#36): M0123-S4 sub-slice 22 — explicit typmod-qualified numeric cast
`::numeric(p,s)`. LANDED + committed. First LENGTH-coercion cast: PG's
coerce_to_target_type wraps the operand (coerced to numeric) in numeric(numeric,
int4)=funcid 1703 (funcformat 1) whose 2nd arg is an int4 Const =
numerictypmodin(p,s)=((p<<16)|(s&0x7ff))+4. numeric(10,2)=655366, numeric(10,0)/
numeric(10)=655364. Emitted canonically ONLY when the column typmod matches the
cast (else PG wraps in a RelabelType, not modeled → degrade).

Files: internal/pgnodes/resolver_expr.go (resolveNumericTypmodCast +
numericTypmodValue; NEW ResolveForColumnTypmod (old ResolveForColumn delegates
with -1) + NumericColumnTypmod); rebuild.go (numericCastPackedTypmod +
numericTypmodCastPS + rebuildFuncExprWith 1703 arm→CastExpr{numeric,[p,s]});
internal/executor/sys_pg_attrdef.go (writer threads col.Type.Args→typmod);
cast_test.go (+3 goldens, colTypmod field, degrade case reframed);
internal/testport/oracle_pgnodes_adbin_test.go (+3, now 52, numericColSQLTypmod).
Design 0123-0005 §"Sub-slice 22" + Deferred; README index addendum.

Gates GREEN: full pgnodes pkg; cast_test 3 new goldens (golden/codec/rebuild);
adbin oracle 52/52 byte-identical vs LIVE PG18.3 (1.57s); initdb reload +
executor attrdef siblings green; go build ./..., go vet, gofmt clean; pgbench
smoke via pre-commit.

Next (M0123-S4 REMAINING): (1) RelabelType IR node (ir.go codec Out/Read +
rebuild) so `::numeric(p,s)` on a BARE/mismatched numeric column canonicalizes
(wrap 1703 in RelabelType to the column typmod) — resume ResolveForColumnTypmod
(emit wrapper instead of degrade when castTypmod != targetTypmod); (2) float4-
common (no float8) CASE result mix — int/numeric→float4 arms + outer float8(float4)
column cast (selectCaseCommonType/coerceCaseResult); (3) date-time-family CASE
coercion — needs a `date` OID-1082 datum (datum.go) + `::date`/`::timestamptz`
string-literal fold; (4) other length types (varchar(N)=CoerceViaIO, timestamp(N),
bit(N)); (5) operator-driven view-qual coercion.
