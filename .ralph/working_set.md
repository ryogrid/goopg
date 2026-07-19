(idle — nothing in flight)

Last loop (#42): M0123-S4 sub-slice 28 — string-literal cast folds to bool/int2/
int4/int8. LANDED + committed. An unknown-type STRING literal coerced to
bool/int2/int4/int8 — explicit `::T` cast OR typed column context (`col int4
DEFAULT '123'`) — folds at parse time to a by-value Const via the type input
function (int4in/int8in/int2in/boolin), NO cast node, byte-identical to PG18.3.
Closes sub-slice 27's `'123'::int4`/`'t'::bool` deferral. KEY BOUNDARY (live-probed):
bare integer `int2 DEFAULT 5` is an int4→int2 cast FuncExpr (funcid 314), NOT an
int2 Const — only the unknown-STRING form folds, so foldStringLiteralConst fires
only on *parser.StringConst; resolveIntLiteral untouched.

Files: internal/pgnodes/datum.go (NewInt2Const, parseIntFromString=pg_strtoint
decimal subset, parseBoolLiteral=parse_bool_with_len port, pgTrimSpace);
resolver_expr.go (shared foldStringLiteralConst routes BOTH resolve StringConst arm
+ resolveCastExpr string block); rebuild.go (OidInt2→StringConst so re-resolve
re-folds; int4/int8/bool keep existing rebuild). NEW string_cast_test.go; oracle
adbin +8 (now 67); executor sys_pg_attrdef_test +6; cast_test/resolver_expr_test
sibling reconciliations. Design 0123-0005 §"Sub-slice 28" + fix_plan + ledger.

Gates GREEN: full pgnodes pkg; adbin oracle 67/67 byte-identical vs LIVE PG18.3;
executor TestCanonicalAttrdefText; go build ./..., go vet (pgnodes/executor/
testport), gofmt clean (resolver_expr_test.go block-246 flag is pre-existing
go1.25-vs-go1.26.3 mismatch, NOT mine — never gofmt -w); pgbench smoke via pre-commit.

Nightly triage (run 20260719-094219, sha c217c692): all 5 AI items already [x]
stale (predate HEAD). No new nightly work.

Next (M0123-S4 REMAINING): (1) text/numeric/float/oid string-literal folds
(`'x'::text`/`'5'::numeric`/`'5'::float8` — add OidText/OidNumeric/OidFloat*/OidOid
arms to foldStringLiteralConst; float needs a float datum + float*in parse); (2)
bare-integer→int2 implicit cast FuncExpr (`int2 DEFAULT 5`, funcid 314 — needs a
resolveIntLiteral int2-context arm emitting the cast, NOT a bare Const); (3)
float4-common (no float8) CASE mix (selectCaseCommonType/coerceCaseResult); (4)
operator-driven view-qual coercion (resolver_query.go); (5) other length types
(varchar(N)=CoerceViaIO, timestamp(N), bit(N)); (6) broader date input forms
(infinity/-infinity, BC years, DateStyle MDY/DMY, textual month — datum.go
parseDateDays).
