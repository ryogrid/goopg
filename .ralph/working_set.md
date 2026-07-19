(idle — nothing in flight)

Last loop (#45): M0123-S4 sub-slice 29c — string-literal cast folds to oid/float4/float8.
LANDED + committed. `'5'::oid` / `'5'::float8` / `'5.5'::float4` (and typed column DEFAULT,
e.g. `col float8 DEFAULT '5.5'`) now fold at parse time via oidin/float4in/float8in to a
by-value Const with NO cast node — 10 oracle cases byte-identical vs LIVE PG18.3 (now 85
adbin total). Closes item (3)/resume-(c) of the sub-slice 29/29b ledger rows.

Files: internal/pgnodes/datum.go (NewOidConst zero-extend / NewFloat8Const raw IEEE bits /
NewFloat4Const sign-extend + parseOidFromString + parseFloat8/4FromString sharing
isDecimalFloatText; +math import); resolver_expr.go (3 foldStringLiteralConst arms
OidOid/OidFloat4/OidFloat8); rebuild.go (3 rebuildConst cases → StringConst re-fold; +math).
Tests: string_float_oid_cast_test.go (8 goldens + codec + cast==col pairs + rebuild fixed
point + BadDegrade); oracle_pgnodes_adbin_test.go (+10, now 85); resolver_expr_test.go /
cast_test.go sibling reconciliations. Design 0123-0005 §"Sub-slice 29c" + fix_plan + ledger.

Gates GREEN: full pgnodes pkg; adbin oracle 85/85 byte-identical vs LIVE PG18.3; executor
TestCanonicalAttrdefText/ResolveForColumn + initdb attrdef; go build ./..., go vet
(pgnodes/testport/executor), gofmt clean; pgbench smoke via pre-commit; ralph-state-guard OK.

Next (M0123-S4 REMAINING): float SPECIALS (`'Infinity'::float8`/`'NaN'::float4` → IEEE
inf/nan Const, mirror parseNumericSpecial recognition); typmod'd string numeric cast
`'5.5'::numeric(10,2)` (resolveNumericTypmodCast should special-case a *parser.StringConst
operand: numeric_in then in-place length coercion, no cast node); the bare-integer→int2
implicit cast FuncExpr (funcid 314, `int2 DEFAULT 5`); float4-common CASE mix; operator-driven
view-qual coercion (resolver_query.go); other length types (varchar(N), timestamp(N), bit(N));
broader date input forms (infinity/BC/DateStyle).
