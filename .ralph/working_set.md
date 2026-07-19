(idle — nothing in flight)

Last loop (#44): M0123-S4 sub-slice 29b — numeric specials NaN/±Infinity fold.
LANDED + committed. `'NaN'/'Infinity'/'-Infinity'::numeric` (and numeric column DEFAULT)
now fold to a canonical digitless NUMERIC_SPECIAL 6-byte varlena (n_header
0xC000/0xD000/0xF000, high byte prints signed -64/-48/-16) instead of degrading — 3
oracle cases byte-identical vs LIVE PG18.3 (now 75 total). Closes item (1)/resume-(a)
of the sub-slice 29 ledger row.

Files: internal/pgnodes/datum.go (numericVar.special field + parseNumericSpecial exact
numeric_in port + varlena/decodeNumericVar/specialText + consts numericExtSignMask/NaN/
PInf/NInf + ciHasPrefix); rebuild.go (OidNumeric special→StringConst spelling, fixed
point); resolver_expr.go (fold comment). Tests: string_text_numeric_cast_test.go (+3
goldens, SpecialsFold 10-spelling matrix, BadDegrade reject matrix), executor
sys_pg_attrdef_test.go (str-numeric-nan flipped canonical), oracle +3. Design 0123-0005
§"Sub-slice 29b" + README + fix_plan + ledger.

Gates GREEN: full pgnodes pkg; adbin oracle 75/75 byte-identical vs LIVE PG18.3;
executor TestCanonicalAttrdefText; go build ./..., go vet (pgnodes/testport/executor),
gofmt clean; pgbench smoke via pre-commit; ralph-state-guard OK.

Next (M0123-S4 REMAINING): (b) typmod'd string numeric cast `'5.5'::numeric(10,2)`
(resolveNumericTypmodCast should special-case a *parser.StringConst operand: numeric_in
then in-place length coercion, no cast node); (c) `::oid`/`::float4`/`::float8` string
folds (need NewOidConst + a by-value float8 datum + float*in Ryu-inverse parse); the
bare-integer→int2 implicit cast FuncExpr (funcid 314, `int2 DEFAULT 5`); float4-common
CASE mix; operator-driven view-qual coercion (resolver_query.go); other length types
(varchar(N), timestamp(N), bit(N)); broader date input forms (infinity/BC/DateStyle).
