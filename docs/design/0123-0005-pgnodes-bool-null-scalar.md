# 0123-0005 — canonical `BoolExpr` / `NullTest` scalar nodes (M0123-S4, sub-slice 1)

Status: accepted
Milestone: M0123 — canonical `pg_node_tree` serialization (`wal-pg-nodetree`)
Depends on: 0123-0001 (scalar codec), 0123-0002 (scalar resolver + rebuild),
0123-0003 (attrdef writer/reload wiring)

## Goal

Extend the `internal/pgnodes` scalar subset with the two boolean-valued node
shapes that dominate real column DEFAULTs, CHECK-style predicates and (later)
view `WHERE` quals: `BoolExpr` (`AND`/`OR`/`NOT`) and `NullTest`
(`IS [NOT] NULL`). Because the scalar path is already wired end-to-end through
`pgnodes.ResolveForColumn` → `canonicalAttrdefText` (writer) and
`pgnodes.Read` → `Rebuild` (reload), a `bool`-typed column DEFAULT such as
`b bool DEFAULT (a IS NOT NULL)` or `f bool DEFAULT (x AND y)` now stores a
**canonical PG18 `pg_node_tree`** in `pg_attrdef.adbin` (a real PG18 standby can
`stringToNode` and EVALUATE it) instead of degrading to SQL text.

This is a codec + resolver + rebuild slice landed together (the sibling paths
change in one commit per the project's encode↔decode↔resolve↔rebuild rule); it
does NOT wire into the view-query resolver (`resolver_query.go`), which resolves
`WHERE` quals through its own scope — that is the next sub-slice.

## The two nodes

### `NullTest` (generated `_outNullTest`)

```
{NULLTEST :arg <node> :nulltesttype <0|1> :argisrow <bool> :location -1}
```

`nulltesttype` is the `NullTestType` enum (`IS_NULL=0`, `IS_NOT_NULL=1`,
`primnodes.h`). `argisrow` is `false` for the scalar subset (a row-valued
`IS NULL` sets it `true`, which this slice never emits — goopg's `IsNullExpr`
carries no row form here). The result is always boolean.

### `BoolExpr` (custom `_outBoolExpr`)

```
{BOOLEXPR :boolop <and|or|not> :args (<nodes>) :location -1}
```

`BoolExpr` is a `custom_read_write` node: `outfuncs.c:_outBoolExpr` writes
`:boolop` as a **bare token** `and`/`or`/`not` (a do-it-yourself enum
representation), NOT as an integer, and `_readBoolExpr` parses it by hand.
`boolopToken`/`readBoolExpr` reproduce exactly this, and reject an unrecognized
token with a clean error (the "not canonical → keep SQL text" signal).

## The flattening trap (load-bearing)

`AND`/`OR` are **n-ary** in PG. `a AND b AND c` parses left-associatively as
`(a AND b) AND c`, and `gram.y:makeAndExpr` collapses that into ONE `BoolExpr`
with three args on sight (it appends the right operand whenever the left is
already a same-`boolop` `BoolExpr`). goopg has no n-ary boolean node — it parses
the same input into a left-nested chain of `BinaryOp{OpAnd}`.

So `resolveBoolBinary` must reproduce `makeAndExpr`: resolve the left operand,
and if it resolved to a `BoolExpr` of the same `boolop` (the left-nested spine),
**append** the right operand to its args rather than nesting. A parenthesised
right side (`a AND (b AND c)`) is a distinct goopg parse tree and stays nested,
exactly as PG keeps it nested (`makeAndExpr` only flattens `lexpr`). Golden case
`(1<2) AND (3>2) AND (5=5)` — a single three-arg `BOOLEXPR` of three `OpExpr`
args — pins this against a live server, and `TestBoolExprNestedRightNotFlattened`
pins the nested-right counter-case.

`rebuildBoolExpr` is the exact inverse: an n-arg `AND`/`OR` folds back into a
LEFT-nested `BinaryOp` chain (`[a b c]` → `(a AND b) AND c`) — the same tree PG
flattened on the way in — so `resolve → Rebuild → re-resolve` is a fixed point.
`NOT` is a single-arg `BOOLEXPR` ↔ `UnaryOp{OpNot}`.

## Resolver / rebuild dispatch added

- `resolve`: `*parser.BooleanConst` → `NewBoolConst` (bool `Const`, type 16);
  `*parser.IsNullExpr` → `resolveNullTest`; `UnaryOp{OpNot}` → `resolveBoolNot`;
  `BinaryOp{OpAnd|OpOr}` routes to `resolveBoolBinary` *before* the operator-seed
  lookup (`AND`/`OR` are not `pg_operator` rows).
- `rebuildConst`: new `OidBool` case → `*parser.BooleanConst` (word `!= 0`).
- `Rebuild`: `*BoolExpr` → `rebuildBoolExpr`, `*NullTest` → `rebuildNullTest`.

`SupportsExpr` is a thin wrapper over `ResolveExpr`, so it picks up the new
shapes with no change; `ResolveForColumn`'s exact-type-match gate accepts them
because both resolve to `bool` (OID 16).

## Gates

`internal/pgnodes/bool_null_test.go` (all green), every `want` captured verbatim
from a live PG18.3 server (`pg_attrdef.adbin == nodeToString`):

- `TestBoolNullResolveMatchesGolden` — parse → `ResolveExpr` → `Out` byte-for-byte
  == real-PG adbin for all six goldens, plus `ResolveForColumn(_, bool)` accepts
  each as canonical.
- `TestBoolNullCodecRoundTrip` — `Read(golden)` → re-`Out` reproduces the bytes.
- `TestBoolNullResolveRebuildRoundTrip` — `resolve → Rebuild → re-resolve` is a
  `reflect.DeepEqual` fixed point (the flattening inverse).
- `TestBoolExprNestedRightNotFlattened` — parenthesised right side stays nested.
- `TestReadRejectsBadBoolop` — corrupt `:boolop` token errors cleanly.

`go build ./...` + `go vet ./internal/pgnodes ./internal/executor ./internal/initdb`
clean; the standard pre-commit pgbench smoke runs on the commit.

## Sub-slice 2 (2026-07-19): view-query `WHERE`/target wiring

The scalar helpers above are now recursion-injectable so the query-scoped
resolver/rebuild reuse them over base-relation columns, mirroring how
`rebuildOpExpr`/`rebuildFuncExpr` were made injectable in 0123-0004 sub-slice 2b.

- `resolver_expr.go`: `resolveBoolBinary`/`resolveBoolNot`/`resolveNullTest`
  become thin wrappers over `*With` variants that take a
  `scopedResolve = func(parser.Expr, uint32) (Node, uint32, error)` recursion.
  The scalar path threads its own `resolve`; the query path threads
  `queryScope.resolveExpr`. Same builder → byte-identical `BOOLEXPR`/`NULLTEST`.
- `resolver_query.go`: `queryScope.resolveExpr` gains `*parser.BooleanConst`,
  `*parser.IsNullExpr` (→ `resolveNullTestWith`), `UnaryOp{OpNot}` (→
  `resolveBoolNotWith`), and an `AND`/`OR` dispatch (→ `resolveBoolBinaryWith`)
  *before* the operator-seed lookup — exactly the scalar dispatch, now over Vars.
- `rebuild.go`: `rebuildBoolExpr`/`rebuildNullTest` gain `*With` variants taking a
  `func(Node) (parser.Expr, error)` recursion; `rebuild_query.go`'s
  `viewRebuildScope.rebuildExpr` adds `*BoolExpr`/`*NullTest` cases routing
  through them so a bool/null operand may itself be a column `Var`.

A multi-condition view (`... WHERE src IS NOT NULL AND client > 0`) now emits a
canonical `pg_rewrite.ev_action` and sets `pg_class.relhasrules=true` (was
SQL-text fallback + `relhasrules=false`).

### Gates (all green)

- `internal/pgnodes/view_bool_null_test.go` — two live-captured PG18.3
  `ev_action` goldens (`v3`: `AND` over `NULLTEST`+`OPEXPR`; `v4`: `OR` over a
  nested `NOT` `BOOLEXPR` + `NULLTEST`): forward resolve→`OutRuleAction`
  byte-for-byte, `Out`→`Read`→`Out` codec round-trip, resolve→`RebuildViewQuery`
  →re-resolve fixed point, and a structural spot-check (AND/OR/NOT shape,
  `IsNotNull`, nested-NOT-not-flattened, rebuilt compound `WHERE`).
- `internal/testport/e2e_failover_goopg_to_pg_test.go` — a real PG18 standby
  reports `relhasrules=true` for `b5c_view2` and PARSES its canonical bool/null
  `ev_action` via `pg_get_viewdef` (reconstructs `src IS NOT NULL` + `client > 0`).

## Sub-slice 3 (2026-07-19): canonical `numeric` (OID 1700) Const datums

A decimal / scientific literal (`parser.NumericConst`) now resolves to a numeric
`Const` whose `constvalue` is the packed on-disk `NumericData` varlena, byte-for-
byte identical to what PostgreSQL 18.3 stores in `adbin` / `ev_action`.

### The on-disk format (`internal/pgnodes/datum.go`)

`numeric` is a non-collatable varlena (`constcollid 0`, `constlen -1`,
`constbyval false`). `outfuncs.c:outDatum` dumps the whole varlena — including
the 4-byte length header — one signed byte at a time, so reproducing the exact
in-memory bytes an x86-64 PG holds is what makes the tree byte-identical. The
encoder mirrors `numeric.c` faithfully:

1. `parseNumericVar` = `set_var_from_str` + `strip_var` (NBASE = 10000,
   `DEC_DIGITS = 4`): parse sign / decimal point / exponent to a decimal
   `dweight`+`dscale`, convert to base-10000 digits, then strip leading/trailing
   zero NBASE digits (a zero value forces `weight 0` / `POS`).
2. `varlena` = `make_result_opt_error`: emit `VARSIZE << 2` (little-endian, the
   `va_4byte` header form) then either the **short** header (`uint16 n_header` =
   `NUMERIC_SHORT | sign | (dscale << 7) | weight-6-bit`, chosen when
   `dscale ≤ 63` and `weight ∈ [-64, 63]`) or the **long** header
   (`uint16 n_sign_dscale` + `int16 n_weight`), followed by the `int16`
   base-10000 digits — all little-endian.
3. `decodeNumericVar` + `text` = the inverse (`get_str_from_var`) for rebuild,
   preserving `dscale` trailing zeros so `100.50` re-encodes identically (does
   NOT collapse to `100.5`).

A folded negative (`- 2.5`) resolves via the `OpUnaryNeg`→`NumericConst` branch
to a single negative `Const` (gram.y's `doNegate`), and rebuilds back to
`UnaryOp{-, NumericConst}` — the parser's own tagging.

### Discovery: integer-valued numeric defaults are NOT numeric Consts

`numeric DEFAULT 0` / `DEFAULT 12345` are typed **int4** by the scanner and
wrapped in an `int4_numeric` cast `FuncExpr` (funcid 1740) — only a literal with
a decimal point / exponent is typed numeric. goopg does not yet emit that
implicit-cast FuncExpr, so those defaults still degrade to SQL text (deferred;
see ledger).

### Gates (all green)

- `internal/pgnodes/numeric_test.go` — six live-captured PG18.3 scalar `adbin`
  goldens (`100.50`, `0.001`, `9999.9999`, `3.14159265358979`, `-2.5`, `1E-10`):
  forward resolve→`Out` byte-for-byte, `Read`→`Out` codec round-trip,
  resolve→`Rebuild`→re-resolve fixed point, exact rebuilt-literal-text +
  dscale-preservation check, and negative-Const rebuild shape.
- Same file — a live-captured `ev_action` view golden (`vn`: `amount > 100.50 AND
  rate < 0.001` over numeric columns, OPEXPR opno 1756/1754): forward
  `OutRuleAction` byte-for-byte, codec round-trip, and `RebuildViewQuery` fixed
  point.

## Sub-slice 4a (2026-07-19): the implicit `int`→`numeric` cast FuncExpr

Closes the sub-slice-3 discovery above. A bare **integer** literal in a numeric
column context (`numeric DEFAULT 0` / `12345` / `-5` / `5000000000`) is typed
`int4`/`int8` by the scanner (`make_const`), then `coerce_to_target_type` wraps
it in an implicit-cast `FuncExpr` — `int4_numeric` (funcid **1740**) or
`int8_numeric` (funcid **1781**) — with `funcformat` **2** (`COERCE_IMPLICIT_CAST`)
and no collation. So PG's `adbin`/`ev_action` is a `FuncExpr` over an `int4`/`int8`
`Const`, never a numeric `Const`. (A literal is never `int2`, so `int2_numeric`
1782 never arises here; `int2` column targets stay SQL-text.)

### Forward (`resolver_expr.go`)

`resolveIntLiteral(v, expected)` now, when `expected == OidNumeric`, wraps the
`int4`/`int8` `Const` it already builds in `wrapIntToNumericCast` (funcid
1740/1781, result 1700, funcformat 2). Any other context keeps the bare `Const`.
The negative fold is unchanged and happens **before** the cast (`-5` → `int4`
Const `-5` inside the FuncExpr), matching `doNegate` then `coerce_type`.

### Reload inverse (`rebuild.go`)

`isImplicitIntToNumericCast` recognises the exact FuncExpr shape (funcid
1740/1781, funcformat 2, result 1700, one arg); `rebuildFuncExprWith` then
rebuilds it to the **inner** integer literal (not a spurious `numeric(<int>)`
call), so a re-resolve re-wraps byte-identical bytes (fixed point). Handled in
the shared `*With` recursion so a view qual carrying the same cast rebuilds too.

### Gates (all green)

- `internal/pgnodes/numeric_cast_test.go` — five live-captured PG18.3 `adbin`
  goldens (`12345`, `0`, `-5`, `5000000000`, `32767`): forward resolve→`Out`
  byte-for-byte, `ResolveForColumn` now ACCEPTS them as canonical, `Read`→`Out`
  codec round-trip, resolve→`Rebuild`→re-resolve fixed point, a rebuilt-shape
  check (integer literal / `UnaryOp`, never a `FuncCall`), and an int-context
  guard (no wrap when the column is `int4`).
- Sibling gates reconciled: `resolver_expr_test.go` `TestResolveForColumn`,
  `internal/executor/sys_pg_attrdef_test.go` `TestCanonicalAttrdefText`, and
  `internal/initdb/catalog_heap_reload_attrdef_test.go` `TestRebuildAttrdefExpr`
  all flipped the `numeric DEFAULT 0` case from SQL-text to canonical FuncExpr.
- `TestE2E_FailoverGoopgToPG` (a real PG18 standby) still green.

## Sub-slice 4b — canonical `timestamptz` (OID 1184) Const datums

A `timestamptz` `DEFAULT` literal is **not** stored as a cast expression: PG's
`coerce_type` folds an "unknown" string literal to the target type at parse time
(`stringTypeToConst` → `timestamptz_in`), so `pg_attrdef.adbin` holds a folded
by-value `Const` — a signed `int64` of microseconds relative to
`POSTGRES_EPOCH_JDATE` (2000-01-01 UTC), `constlen 8` / `constbyval true` /
`consttype 1184`. Same 8-byte `outDatum` word as the integer Consts (a pre-2000
value sign-extends to all-`0xFF` high bytes).

### Datum codec (`datum.go`)

`NewTimestamptzConst(usec)` builds the by-value Const. `parseTimestamptzMicros`
converts a literal to μs using PG's exact integer `date2j` (Gregorian → Julian
day, `datetime.c`) — no floating point, correct across the proleptic calendar.
The **deterministic subset only**: an ISO date+time with an EXPLICIT numeric
offset (or `Z`), plus the `epoch` keyword. A bare date or an offset-less
date+time is `TimeZone`-GUC-dependent, so it returns `ok=false` and the writer
degrades to SQL text (all-or-nothing; 02e §3) — a standby must never disagree
with goopg on the folded instant. `formatTimestamptzUTC` (via the inverse
`j2date`) renders μs back to a canonical `YYYY-MM-DD HH:MM:SS[.ffffff]+00`
literal for the rebuild path; re-resolving it (explicit `+00`) reproduces the
identical datum (fixed point).

### Forward + reload (`resolver_expr.go`, `rebuild.go`)

The `StringConst` case gains an `expected == OidTimestamptz` branch that folds
via `parseTimestamptzMicros`. `rebuildConst` gains an `OidTimestamptz` case that
renders the datum back to a `StringConst` UTC literal. Scalar (column DEFAULT)
scope only — a string literal compared to a `timestamptz` column in a view qual
needs operator-driven coercion (the `>` operand type inferred from the Var),
which stays deferred.

### Gates (all green)

- `internal/pgnodes/timestamptz_test.go` — four live-captured PG18.3 `adbin`
  goldens (`'2024-01-15 10:30:00+00'`, the epoch `'2000-01-01 00:00:00+00'`,
  the `'epoch'` keyword, sub-second negative `'1999-12-31 23:59:59.5+00'`):
  forward resolve→`Out` byte-for-byte, `ResolveForColumn` accepts, `Read`→`Out`
  codec round-trip, resolve→`Rebuild`→re-resolve fixed point; a
  graceful-degradation matrix (no-offset / bare-date / garbage reject, text
  context stays text); and a direct `parseTimestamptzMicros` math table
  (`T`/`Z`, `+05:30`, packed `-0530`, epoch, negatives).
- `internal/executor/sys_pg_attrdef_test.go` `TestCanonicalAttrdefText` gains a
  `timestamptz` literal case (canonical Const) + a no-offset case (SQL text).
- `TestE2E_FailoverGoopgToPG` still green.

## Sub-slice 5 — canonical `BOOLEANTEST` (`x IS [NOT] TRUE/FALSE/UNKNOWN`)

`x IS TRUE` is **not** an operator or NullTest: PG parses it to a dedicated
`BooleanTest` node (`primnodes.h`) with a `booltesttype` enum ordinal, and
`cookDefault` stores it in `pg_attrdef.adbin` unfolded (like `BoolExpr` — no
const-folding of the DEFAULT). The result is always boolean, never NULL.

### IR + codec (`ir.go`, `outfuncs.go`, `readfuncs.go`)

`BooleanTest{Arg, BoolTestType, Location}` mirrors the struct field order, so
`outBooleanTest` writes `{BOOLEANTEST :arg … :booltesttype N :location -1}`.
`booltesttype` is a plain integer ordinal (`WRITE_ENUM_FIELD` writes the `int`,
unlike `BoolExpr`'s `:boolop and` token), N∈0..5 =
`IS_TRUE/IS_NOT_TRUE/IS_FALSE/IS_NOT_FALSE/IS_UNKNOWN/IS_NOT_UNKNOWN`.
`readBooleanTest` reads the three fields symmetrically; it accepts any int and
leaves range-checking to `Rebuild` (a corrupt ordinal degrades the reload to SQL
text rather than mis-parsing).

### Forward + reload (`resolver_expr.go`, `rebuild.go`)

`resolveBooleanTest` maps goopg's `parser.IsBoolExpr` flags
(`TestTrue`/`TestFalse`/`Negated`; neither test flag ⇒ `UNKNOWN`) to the ordinal
via `booleanTestType`, resolving the argument in a `bool` context.
`rebuildBooleanTest` is the exact inverse (ordinal → the three flags), so
resolve→`Rebuild`→re-resolve is a fixed point for all six ordinals. Both have
`…With(rec)` variants threading the injected recursion, so a `BOOLEANTEST` whose
argument is a column `Var` reloads inside a view qual (the same pattern as
`NullTest`/`BoolExpr`).

### Gates (all green)

- `internal/pgnodes/booleantest_test.go` — six live-captured PG18.3 `adbin`
  goldens, one per ordinal 0..5 (`true IS [NOT] TRUE`, `false IS [NOT] FALSE`,
  `(1=1) IS [NOT] UNKNOWN` — the last two prove a non-trivial `OPEXPR` argument):
  forward resolve→`Out` byte-for-byte, `ResolveForColumn` accepts, `Read`→`Out`
  codec round-trip, resolve→`Rebuild`→re-resolve fixed point, plus an
  out-of-range-ordinal `Rebuild`-rejection guard.
- `go build ./...` + `go vet ./internal/pgnodes/` clean; full `pgnodes` package
  and `internal/executor` `TestCanonicalAttrdef*` still green.

## Sub-slice 6 — `BOOLEANTEST` in the VIEW-query path

Sub-slice 5 wired `BOOLEANTEST` only into the scalar (column-DEFAULT) resolver;
a view whose `WHERE` qual used `IS [NOT] TRUE/FALSE/UNKNOWN` still fell back to
SQL text. This slice routes the query-scoped resolver/rebuild through the same
recursion-injectable `…With` builders sub-slice 2 used for `BoolExpr`/`NullTest`:

- `resolver_query.go` — `queryScope.resolveExpr` adds a
  `case *parser.IsBoolExpr: return resolveBooleanTestWith(v, s.resolveExpr)`,
  directly after the `*parser.IsNullExpr` arm. Threading `s.resolveExpr` as the
  recursion lets the `BOOLEANTEST` operand be (or contain) a base-relation column
  `Var`, so `(client > 0) IS TRUE` resolves canonically instead of degrading.
- `rebuild_query.go` — `viewRebuildScope.rebuildExpr` adds a
  `case *BooleanTest: return rebuildBooleanTestWith(v, s.rebuildExpr)`, the exact
  inverse over the view scope, mirroring the `*NullTest` arm.

No new IR, codec, or builder code — only two dispatch arms; the sub-slice-5
`…With` variants already existed for exactly this purpose.

### Gates (all green)

- `internal/pgnodes/view_bool_null_test.go` — two new live-captured PG18.3
  `pg_rewrite.ev_action` goldens over `bench_log` (relid 16384): `v5`
  (`(client > 0) IS TRUE`, `booltesttype 0`) and `v6`
  (`(client > 0) IS NOT FALSE`, `booltesttype 3`, a non-zero ordinal). They join
  the existing table-driven `TestResolveViewQueryBoolNull` (forward byte-for-byte),
  `…RoundTrip` (Out→Read→Out), and `TestRebuildViewQueryBoolNull`
  (resolve→`RebuildViewQuery`→re-resolve fixed point).
- full `pgnodes` package + `go vet ./internal/pgnodes/` + `go build ./...` clean.

## Sub-slice 7 — canonical `CASEEXPR` / `CASEWHEN` (searched form)

A column DEFAULT (or, later, a view qual/target) `CASE WHEN cond THEN result …
[ELSE result] END` now resolves to a canonical PG18 `CaseExpr` tree instead of
degrading to SQL text. Like `BoolExpr`/`BooleanTest`, `cookDefault` stores it in
`pg_attrdef.adbin` unfolded.

### IR + codec (`ir.go`, `outfuncs.go`, `readfuncs.go`)

`CaseExpr{Casetype, Casecollid, Arg, Args, Defresult, Location}` mirrors the
generated `_outCaseExpr` field order; `CaseWhen{Expr, Result, Location}` mirrors
`_outCaseWhen`. `Args` is a `[]Node` of `*CaseWhen`, so `wNodeList`/
`readNodeListField` handle it exactly as any other node list, and both `outNode`
and `readNode` gained `CASEEXPR`/`CASEWHEN` dispatch arms. `Arg` is always `<>`
(searched form only; see below).

### Forward + reload (`resolver_expr.go`, `rebuild.go`, `datum.go`)

`resolveCaseExprWith` (with a scalar `resolveCaseExpr` wrapper threading the
scalar `resolve`, and the `…With(rec)` recursion for a later view-qual slice)
mirrors `transformCaseExpr` for the **searched** form only:

- Every WHEN condition must resolve to `bool` (`coerce_to_boolean` is not
  reproduced — a non-bool condition degrades to SQL text).
- Every WHEN result and any ELSE must resolve to the **same** non-collatable
  type; that type is `casetype` and `casecollid` is `0`. `caseTypeMeta`
  (`datum.go`) is the allowlist (bool/int2/int4/int8/oid/numeric/timestamptz) —
  a collatable or unmodeled result type returns `ok=false` so the writer never
  emits a wrong non-zero `casecollid` (all-or-nothing; 02e §3).
- When ELSE is omitted, PG synthesizes a typed NULL `Const` of `casetype`
  (`transformCaseExpr` coerces an untyped NULL `A_Const`); `newNullConst`
  reproduces it (`constisnull true`, `constvalue <>`, the type's
  `constlen`/`constbyval`).

`rebuildCaseExprWith` is the exact inverse: a NULL-`Const` `Defresult` rebuilds
to an **omitted** ELSE, so `resolve → Rebuild → re-resolve` re-synthesizes
byte-identical bytes (the fixed point). The simple form (`CASE operand WHEN …`,
which PG expands into a `CaseTestExpr` placeholder + an `operand = val` OpExpr
per WHEN) and cross-type `select_common_type` coercion are **deferred** — both
degrade to SQL text (see ledger).

### Gates (all green)

- `internal/pgnodes/case_test.go` — five live-captured PG18.3 `adbin` goldens
  (int with ELSE, int no-ELSE with the typed NULL defresult, a two-WHEN body
  over `OPEXPR` conditions, numeric, bool): forward resolve→`Out` byte-for-byte,
  `ResolveForColumn` accepts, `Read`→`Out` codec round-trip, resolve→`Rebuild`
  →re-resolve fixed point, plus a graceful-degradation matrix (mixed-type,
  simple form, text-result all stay SQL text).
- `internal/executor/sys_pg_attrdef_test.go` `TestCanonicalAttrdefText` gains a
  canonical `case-expr` + `case-no-else` case (flipped from SQL-text) and a
  `case-mixed` SQL-text case.
- `go vet ./internal/pgnodes/` + `go build ./...` + `gofmt -l` clean; full
  `pgnodes` + `internal/initdb` packages green.

## Sub-slice 8 — `CASEEXPR` in the VIEW-query path

Sub-slice 7 wired `CASEEXPR`/`CASEWHEN` only into the scalar (column-DEFAULT)
resolver; a view whose `WHERE` qual (or, in scope, target) used a searched
`CASE` still fell back to SQL text. This slice routes the query-scoped
resolver/rebuild through the same recursion-injectable `…With` builders
sub-slice 7 already exposed for exactly this purpose (mirroring sub-slice 6's
`BOOLEANTEST` wiring):

- `resolver_query.go` — `queryScope.resolveExpr` adds a
  `case *parser.CaseExpr: return resolveCaseExprWith(v, s.resolveExpr)`, directly
  after the `*parser.IsBoolExpr` arm. Threading `s.resolveExpr` as the recursion
  lets every `WHEN` condition and result operand be (or contain) a base-relation
  column `Var`, so `CASE WHEN client > 0 THEN true ELSE false END` resolves
  canonically instead of degrading. The searched-form-only / same-casetype /
  `caseTypeMeta` allowlist guards live inside `resolveCaseExprWith`, so the simple
  form and mixed-type CASE still return `ErrUnsupported` → SQL text.
- `rebuild_query.go` — `viewRebuildScope.rebuildExpr` adds a
  `case *CaseExpr: return rebuildCaseExprWith(v, s.rebuildExpr)`, the exact inverse
  over the view scope. The omitted-`ELSE` ↔ synthesized-NULL-`Const` fixed point
  (sub-slice 7) is preserved through the view path unchanged.

No new IR, codec, or builder code — only two dispatch arms.

### Gates (all green)

- `internal/pgnodes/view_bool_null_test.go` — two new live-captured PG18.3
  `pg_rewrite.ev_action` goldens over `bench_log` (relid 16384): `v7`
  (`CASE WHEN client > 0 THEN true ELSE false END` — one `WHEN` + explicit
  `ELSE`, `casetype 16`) and `v8`
  (`CASE WHEN src IS NULL THEN false WHEN client > 0 THEN true END` — two
  `WHEN`s + omitted `ELSE` → typed-NULL `defresult`, `constisnull true`). They
  join the existing table-driven `TestResolveViewQueryBoolNull` (forward
  byte-for-byte), `…RoundTrip` (Out→Read→Out), and `TestRebuildViewQueryBoolNull`
  (resolve→`RebuildViewQuery`→re-resolve fixed point); `TestViewQueryBoolNullStructure`
  gains v7/v8 structural assertions (explicit-ELSE non-null `Const` vs
  omitted-ELSE synthesized-NULL `Const`, and the rebuilt-AST `Else` presence).
- `internal/testport/e2e_failover_goopg_to_pg_test.go` — new `b5c_view3`
  (`… WHERE CASE WHEN client > 0 THEN true ELSE false END`): a real PG18 standby
  reports `relhasrules=true` and `pg_get_viewdef` PARSES the canonical CASE
  `ev_action` back to the `CASE WHEN (client > 0) THEN true ELSE false END`
  SELECT — the adversarial standby proof for the CASE query wiring.
- full `pgnodes` package + `go vet ./internal/pgnodes/` + `go build ./...` clean.

## Deferred

- The `CASE` **simple form** (`CASE operand WHEN …` — CaseTestExpr placeholder)
  and `select_common_type` cross-type result coercion remain in M0123-S4.
- `IS DISTINCT FROM` and the byte-diff oracle gate remain in M0123-S4.
  Operator-driven implicit coercion in view quals (which would let a
  `timestamptz`/`int2`→`numeric` string literal resolve inside a view `WHERE`)
  is also still SQL-text — only the exact scalar-column DEFAULT context folds a
  `timestamptz`/numeric literal.
