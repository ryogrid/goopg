(idle — nothing in flight)

Last loop (#41): M0123-S4 sub-slice 27 — explicit `::date` / `::timestamptz` cast
of a string literal folds to the SAME by-value Const as the bare-literal column
form. LANDED + committed. PG folds an unknown-type string literal at parse time
(coerce_type→stringTypeToConst→type input func) into a Const whose consttype is the
cast target, with NO cast node — so `'2024-03-15'::date` stores a bare DateADT Const
byte-identical to `DEFAULT '2024-03-15'`. Closes the asymmetry where the bare form
was canonical but the explicit-cast form degraded to SQL text.

Files: internal/pgnodes/resolver_expr.go (resolveCastExpr leading date/timestamptz
string-fold arm — parseDateDays/parseTimestamptzMicros; non-string operand / invalid
/ TZ-dependent / typmod'd → ErrUnsupported). NO IR/codec/rebuild change (folded Const
== column-context form; rebuildConst's OidDate/OidTimestamptz arms already invert it).
NEW internal/pgnodes/datetime_cast_test.go (3 goldens w/ UNKNOWN context + cast==bare
pair + degradation matrix + column-scoped reload fixed point); oracle_pgnodes_adbin_
test.go +2 (date_cast/timestamptz_cast → 29 cases); executor sys_pg_attrdef_test.go
+date-cast/tstz-cast/tstz-cast-notz. Design 0123-0005 §"Sub-slice 27" + fix_plan + ledger.

Gates GREEN: full pgnodes pkg; adbin oracle 29/29 byte-identical vs LIVE PG18.3;
executor TestCanonicalAttrdefText; go build ./..., go vet (pgnodes/testport/executor),
gofmt clean; pgbench smoke via pre-commit.

Nightly triage (run 20260719-094219, sha c217c692): all 5 AI items already [x] stale
(predate HEAD). No new nightly work.

Next (M0123-S4 REMAINING): (1) broader literal-cast folds — string→int4/int8/bool
(`'123'::int4`, `'t'::bool`) via pg_atoi/boolin analogues in resolveCastExpr; (2)
float4-common (no float8) CASE mix — int/numeric→float4 arms + outer float8(float4)
column cast (selectCaseCommonType/coerceCaseResult); (3) operator-driven view-qual
coercion (unblocks int2/timestamptz literals inside a view WHERE — resolver_query.go);
(4) other length types (varchar(N)=CoerceViaIO, timestamp(N), bit(N)); (5) broader date
input forms (infinity/-infinity, BC years, DateStyle MDY/DMY, textual month — datum.go
parseDateDays). ALL date/timestamptz literal (bare + explicit-cast) Const shapes canonical.
