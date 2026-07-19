(idle — nothing in flight)

Last loop (#43): M0123-S4 sub-slice 29 — string-literal cast folds to text/numeric.
LANDED + committed. An unknown-type STRING literal coerced to text/numeric — explicit
`::T` cast (`'foo'::text`, `'5.5'::numeric`) OR typed column context (`col numeric
DEFAULT '5.5'`) — folds at parse time via textin/numeric_in to a by-value Const, NO
cast node, byte-identical to PG18.3. Closes sub-slice 28's text/numeric deferral.
KEY: `'5.5'::numeric` is byte-identical to bare `5.5` (same NumericData varlena);
`'5.50'` keeps dscale 2. Only the explicit-`::text`-cast form was actually degrading
(text COLUMN context already handled by resolve's StringConst arm); numeric col +
explicit cast both degraded before.

Files: internal/pgnodes/resolver_expr.go (2 new arms in shared foldStringLiteralConst:
OidText=NewTextConst verbatim always-ok, OidNumeric=NewNumericConst(pgTrimSpace(s))
reusing sub-slice-3 datum). NO rebuild/codec change (text→StringConst, numeric→
NumericConst already re-fold to the fixed point). NEW string_text_numeric_cast_test.go;
oracle adbin +5 (now 72); executor sys_pg_attrdef +4 (incl. str-numeric-nan degrade).
Design 0123-0005 §"Sub-slice 29" + fix_plan + ledger.

Gates GREEN: full pgnodes pkg; adbin oracle 72/72 byte-identical vs LIVE PG18.3
(1.88s); executor TestCanonicalAttrdefText; go build ./..., go vet (pgnodes/testport/
executor), gofmt clean; pgbench smoke via pre-commit; ralph-state-guard OK.

Nightly triage (run 20260719-094219, sha c217c692): all 5 AI items already [x] stale
(fixed at HEAD). No new nightly work.

Next (M0123-S4 REMAINING): (1) numeric specials `'NaN'/'Infinity'/'-Infinity'::numeric`
(special 0xC000-header varlena — NewNumericConst NaN/±Inf fast path); (2) typmod'd
string numeric cast `'5.5'::numeric(10,2)` (resolveNumericTypmodCast should special-case
a StringConst operand: numeric_in then in-place length coercion, no cast node); (3)
`::oid`/`::float4`/`::float8` string folds (need NewOidConst + a float8 datum + float*in
Ryu-inverse parse); (4) bare-integer→int2 implicit cast FuncExpr (`int2 DEFAULT 5`,
funcid 314 — resolveIntLiteral int2-context arm emitting the cast, NOT a bare Const);
(5) float4-common CASE mix (selectCaseCommonType/coerceCaseResult); (6) operator-driven
view-qual coercion (resolver_query.go); (7) other length types (varchar(N), timestamp(N),
bit(N)); (8) broader date input forms (infinity/BC/DateStyle/textual-month).
