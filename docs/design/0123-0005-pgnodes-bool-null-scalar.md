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

## Sub-slice 9 — canonical `DISTINCTEXPR` (`a IS [NOT] DISTINCT FROM b`) scalar node

A column DEFAULT of the shape `bool DEFAULT (a IS [NOT] DISTINCT FROM b)` now
resolves to a canonical `DISTINCTEXPR` (was SQL-text fallback). This mirrors
PG's `transformAExprDistinct` → `make_distinct_op`
(`src/backend/parser/parse_expr.c`): the node is a `make_op` `OpExpr` over the
`=` operator with `NodeSetTag(result, T_DistinctExpr)`. PG relies on
"DistinctExpr and OpExpr being same struct", so the on-disk field list is
byte-identical to `OPEXPR` — only the type token differs. `IS NOT DISTINCT FROM`
is that `DISTINCTEXPR` wrapped in a `NOT` `BOOLEXPR`
(`makeBoolExpr(NOT_EXPR, [DistinctExpr])`).

### IR + codec (`ir.go`, `outfuncs.go`, `readfuncs.go`)

- `ir.go`: `type DistinctExpr OpExpr` — a defined type over `OpExpr`, reproducing
  PG's same-struct relationship; `nodeTag()` returns `"DISTINCTEXPR"`.
- `outfuncs.go`: `outOpExpr` is factored into `outOpExprFields` (the shared field
  writer); `outDistinctExpr` writes the `DISTINCTEXPR` token then the same
  fields via `(*OpExpr)(d)`.
- `readfuncs.go`: symmetric factoring — `readOpExprFields` reads the shared
  fields; `readDistinctExpr` reads into an `OpExpr` and re-tags to
  `*DistinctExpr` (mirroring `_readDistinctExpr`, which reuses the `OpExpr`
  field list).

### Forward + reload (`resolver_expr.go`, `rebuild.go`)

- `resolver_expr.go`: `*parser.IsDistinctFromExpr` → `resolveDistinctFrom`
  (`…With` recursion-injectable so sub-slice 10 can thread
  `queryScope.resolveExpr` for view quals over column Vars). Both operands
  forward-resolve with an unknown context; `buildDistinctExpr` reuses
  `buildOpExpr` for the `=` operator (so `opno`/`opfuncid`/collation are
  byte-identical to a plain `a = b`) and re-tags. `IS NOT DISTINCT FROM` wraps
  the result in a `NOT` `BoolExpr`.
- `rebuild.go`: `*DistinctExpr` → `rebuildDistinctExpr` (`…With` injectable) →
  `IsDistinctFromExpr{Negated:false}`. The `NOT` wrapper is rebuilt by the
  existing `rebuildBoolExpr` `NOT` arm into `NOT (a IS DISTINCT FROM b)` — a
  distinct spelling that re-resolves to the identical `NOT`-`BOOLEXPR`/
  `DISTINCTEXPR` IR, so `resolve→Rebuild→re-resolve` is still a fixed point.

### Gates (all green)

- `internal/pgnodes/distinct_test.go` — five live-captured PG18.3 `adbin`
  goldens (int `=` opno 96, the `IS NOT DISTINCT FROM` `NOT`-`BOOLEXPR` wrapper,
  text `=` opno 98 with `inputcollid 100`, numeric `=` opno 1752, bool `=` opno
  91), each: forward byte-for-byte (`ResolveExpr`→`Out`) + `ResolveForColumn`
  accepts + codec round-trip (`Read`→`Out`) + resolve→`Rebuild`→re-resolve fixed
  point.
- `internal/executor/` default-validation + attrdef sibling tests
  (`TestDefault*`, `TestCanonicalAttrdef*`) unchanged and green (`ok4`
  `1 IS DISTINCT FROM 2` still a valid constant DEFAULT; `bad7` column-ref-in-
  distinct still rejected).
- full `pgnodes` package + `go vet ./internal/pgnodes/` + `go build ./...` clean.

## Sub-slice 10 — `DISTINCTEXPR` view-query wiring

The scalar `DISTINCTEXPR` node from sub-slice 9 is now routed through the
view-query resolver/rebuild so a view `WHERE a IS [NOT] DISTINCT FROM b` over
base-relation columns serializes to canonical `pg_rewrite.ev_action` (was
SQL-text fallback). Purely two dispatch arms — the recursion-injectable
`…With` builders already exist:

- `resolver_query.go`: `queryScope.resolveExpr` gains a
  `*parser.IsDistinctFromExpr` arm → `resolveDistinctFromWith(v, s.resolveExpr)`,
  so both operands resolve in the relation scope (a column becomes a `Var`). The
  `IS NOT DISTINCT FROM` `NOT`-`BOOLEXPR` wrapper is emitted by
  `resolveDistinctFromWith` itself, so no extra arm is needed for the NOT form.
- `rebuild_query.go`: `viewRebuildScope.rebuildExpr` gains a `*DistinctExpr` arm
  → `rebuildDistinctExprWith(v, s.rebuildExpr)`; the `NOT` wrapper rebuilds via
  the existing `rebuildBoolExprWith` `NOT` arm (which re-enters the new
  `DISTINCTEXPR` arm) into `NOT (a IS DISTINCT FROM b)`.

### Gates (all green)

- `internal/pgnodes/view_bool_null_test.go` — two live-captured PG18.3
  `ev_action` goldens (`v9` = `client IS DISTINCT FROM 5` bare `DISTINCTEXPR`
  opno 96 over a column `Var` + `Const`; `v10` = `client IS NOT DISTINCT FROM 5`
  single-arg `NOT` `BOOLEXPR` wrapping it), each: forward byte-for-byte
  (`ResolveViewQuery`→`OutRuleAction`) + codec round-trip
  (`Out`→`Read`→`Out`) + resolve→`RebuildViewQuery`→re-resolve fixed point +
  a structural assertion (`TestViewQueryBoolNullStructure`).
- full `pgnodes` package + `go vet ./internal/pgnodes/` + `go build ./...` clean.

## Sub-slice 11 — `IS [NOT] DISTINCT FROM NULL` → `NullTest` rewrite

An undecorated NULL literal on either side of `IS [NOT] DISTINCT FROM` is no
longer a SQL-text fallback: `resolveDistinctFromWith` now reproduces PG's
`transformAExprDistinct` → `make_nulltest_from_distinct` (`parse_expr.c`).
"If either input is an undecorated NULL literal, transform to a NullTest on the
other input" — this avoids requiring the datatype to have an `=` operator and is
byte-identical to what PG stores:

- The rewrite fires **before** resolving operands (PG tests `exprIsNullConstant`
  on the raw `A_Const`). A new `distinctNullTestArg` helper detects a bare
  `*parser.NullConst` on the right (checked first, matching PG) or the left and
  returns the other operand. A decorated `NULL::type` is a cast node, not a
  `NullConst`, so it correctly takes the ordinary `DISTINCTEXPR` path — exactly
  as PG's `IsA(arg, A_Const)` guard requires.
- `IS DISTINCT FROM NULL` → `NULLTEST` `nulltesttype 1` (IS NOT NULL);
  `IS NOT DISTINCT FROM NULL` → `NULLTEST` `nulltesttype 0` (IS NULL). The
  negation is folded into `nulltesttype`, so there is **no** `NOT` `BOOLEXPR`
  wrapper (contrast sub-slice 10's `DISTINCTEXPR`-under-`NOT`). `argisrow` is
  false regardless of operand type.
- The result is a plain `NullTest`, so the existing rebuild path already
  round-trips it: `RebuildViewQuery` emits `x IS [NOT] NULL`, matching
  `pg_get_viewdef` (`WHERE (client IS NOT NULL)` / `(client IS NULL)`) — a stable
  fixed point that does **not** restore the original `IS DISTINCT FROM` spelling.
  No rebuild-path change was needed. The query-scoped resolver inherits the
  behavior for free (it already dispatches `*parser.IsDistinctFromExpr` →
  `resolveDistinctFromWith`).

### Gates (all green)

- `internal/pgnodes/view_bool_null_test.go` — two more live-captured PG18.3
  `ev_action` goldens (`v11` = `client IS DISTINCT FROM NULL` → `NULLTEST`
  nulltesttype 1; `v12` = `client IS NOT DISTINCT FROM NULL` → `NULLTEST`
  nulltesttype 0, no `NOT` wrapper), each: forward byte-for-byte + codec
  round-trip + resolve→`RebuildViewQuery`→re-resolve fixed point (asserting the
  rebuilt WHERE is `IS [NOT] NULL`, not `IS DISTINCT FROM`) + structural
  assertions in `TestViewQueryBoolNullStructure`.
- full `pgnodes` package + `go vet ./internal/pgnodes/` + `go build ./...` clean.

## Sub-slice 12 — `CASE` **simple form** (`CaseTestExpr` placeholder)

`CASE operand WHEN val THEN … END` now resolves to a canonical `CASEEXPR`
instead of degrading to SQL text. PG's `transformCaseExpr` (parse_expr.c) turns
the simple form into: `newc->arg` = the transformed operand, and each `WHEN val`
expanded via `makeSimpleA_Expr(AEXPR_OP, "=", placeholder, val)` into an OpExpr
`placeholder = val` whose left arg is a `CaseTestExpr` typed from the operand
(`typeId`/`typeMod`/`collation` = `exprType`/`exprTypmod`/`exprCollation` of the
operand). The deparse inverse (ruleutils `get_rule_expr`) recognizes that shape
and prints just the OpExpr RHS as the WHEN value.

### New node — `CaseTestExpr` (generated `_outCaseTestExpr`)

`{CASETESTEXPR :typeId <oid> :typeMod <int> :collation <oid>}` — added to
`ir.go` (struct + `nodeTag`), `outfuncs.go` (`outCaseTestExpr` + dispatch), and
`readfuncs.go` (`readCaseTestExpr` + `"CASETESTEXPR"` dispatch). It appears only
as the left arg of the per-WHEN OpExpr; it never stands alone.

### Forward + reload (`resolver_expr.go`, `rebuild.go`)

- `resolveCaseExprWith`: when `e.Operand != nil`, resolve the operand once into
  `Arg`, extract `(typmod, collid)` via `operandTypmodCollid` (Var → vartypmod/
  varcollid, Const → consttypmod/constcollid; any other operand shape degrades),
  and build the `CaseTestExpr` placeholder. `resolveCaseWhenCond` builds each arm:
  searched form keeps the bool condition; simple form emits
  `buildOpExpr(placeholder, argType, val, valType, "=")`. `buildOpExpr` requires
  an **exact** `(operandType = valType)` operator (the index is keyed on exact
  left/right OIDs), so the placeholder is never wrapped in an implicit coercion —
  matching the shape ruleutils recognizes; a WHEN value needing coercion degrades
  to SQL text.
- `rebuildCaseExprWith`: for `Arg != nil` it restores `Operand` and, via
  `rebuildCaseWhenCond`, unwraps each arm's OpExpr and rebuilds only its second
  arg (the WHEN value); the `CaseTestExpr` placeholder is dropped (the operand is
  rebuilt separately). A `re-resolve` reproduces identical bytes (fixed point).

### Gates (all green)

- `internal/pgnodes/case_test.go` — four live-captured PG18.3 scalar `adbin`
  goldens (`simple_int_else`, `simple_int_two_when_no_else` (synthesized NULL
  defresult), `simple_numeric_else`, `simple_int_two_when_else`), each through
  the golden/codec/rebuild-fixed-point loops; `TestCaseDegradesGracefully` now
  covers a simple-form mixed-result-type CASE (still SQL text).
- `internal/pgnodes/view_bool_null_test.go` — `v13` (`CASE client WHEN 5 THEN
  true ELSE false END`) live `ev_action` golden exercising the **Var-operand**
  path (`:arg` a VAR, `CaseTestExpr` typed from vartypmod/varcollid): forward +
  codec round-trip + rebuild fixed point + a structural assertion (`Arg` is a
  Var, arm cond is `CaseTestExpr = 5` OpExpr, rebuilt WHERE is a simple CASE with
  operand set).
- full `pgnodes` package + `go vet ./internal/pgnodes/` + `go build ./...` clean.

## Sub-slice 13 — `CASE` cross-type result coercion (`select_common_type`)

A `CASE` whose WHEN/ELSE results differ in type previously degraded to SQL text.
PG's `transformCaseExpr` (`parse_expr.c`) instead runs `select_common_type`
(`parse_coerce.c`) over the result list (every `WHEN … THEN` result plus the
`ELSE`, or the synthesized `UNKNOWN` NULL when `ELSE` is omitted), sets
`casetype` to the winner, then calls `coerce_to_common_type` on each result to
insert a cast where the result type differs from the common type. The stored
`adbin`/`ev_action` keeps those casts **un-const-folded** (parse-analysis output,
not planner output), exactly as sub-slice 4a established for a top-level default.

This slice models the bounded **numeric family** — the common cross-type case:

- `selectCaseCommonType(resultTypes)` (`resolver_expr.go`): all-same → that type;
  a mix drawn from `{int4,int8,numeric}` that includes `numeric` → `numeric`
  (reproducing PG's preferred-type walk, where an integer implicitly coerces to
  numeric but not the reverse, so numeric wins). Any other mix — notably an
  `int4`+`int8` mix with no numeric, whose PG common type `int8` needs an
  int→int width cast — returns false so the CASE stays SQL text.
- `coerceCaseResult(node, from, to)`: identity when `from == to`; an `int4`/`int8`
  result whose common type is `numeric` is wrapped in the already-existing
  implicit `int_numeric` cast `FuncExpr` (`wrapIntToNumericCast`: `int4_numeric`
  1740 / `int8_numeric` 1781, funcformat 2), byte-identical to PG.
- `resolveCaseExprWith` now resolves all conditions + results **first** (un-coerced,
  collecting result types), selects the common type, then wraps each result — so
  the cast can land on a `WHEN` result, on the `ELSE`, or on several arms of a
  multi-arm CASE, matching PG's per-result coercion.

Rebuild is unchanged: `rebuildCaseExprWith` recurses through each result via the
existing `rebuildFuncExprWith` → `isImplicitIntToNumericCast` (sub-slice 4a),
which unwraps the cast back to the inner integer literal — so a mixed CASE is a
fixed point (`THEN 1 ELSE 2.5` → `int4_numeric(1)` / numeric → rebuilds to
`THEN 1 ELSE 2.5`).

### Gates (all green)

- `internal/pgnodes/case_test.go` — four live-captured PG18.3 scalar `adbin`
  goldens through the golden/codec/rebuild-fixed-point loops:
  `crosstype_int_then_numeric_else` (cast on `WHEN`), `crosstype_simple_int_numeric`
  (simple form), `crosstype_numeric_then_int_else` (cast on `ELSE`),
  `crosstype_int8_int4_numeric` (a multi-arm CASE with both an `int8_numeric` and
  an `int4_numeric` cast). `TestCaseDegradesGracefully` now covers the two
  boundaries that stay SQL text: a collatable (`text`) result and an
  integer-width-only (`int4`+`int8`, no numeric) mix.
- `internal/executor/sys_pg_attrdef_test.go` — the `case-mixed` sibling flips from
  SQL text to canonical (`casetype 1700`, `FUNCEXPR 1740`).
- full `pgnodes` package + `go vet ./internal/pgnodes/` + `go build ./...` clean;
  executor `TestCanonicalAttrdefText`/`TestDefault` green.

## Sub-slice 14 — `CASE` cross-FAMILY integer result coercion (`int4`→`int8`)

Sub-slice 13 folded a mixed `CASE` only when the common type was `numeric`; a mix
of `int4`+`int8` **with no numeric** still degraded to SQL text because its PG
common type `int8` needs an int→int *width* cast (not the int→numeric cast 13
modeled). This slice closes that gap — the last member of the exact-integer /
numeric family.

`select_common_type` (`parse_coerce.c`) walks the result list: none of `int4`,
`int8`, `numeric` is a *preferred* type (`typispreferred=f`; only `float8` is
preferred in the numeric category), so for each pair the walk advances to whichever
type the narrower one implicitly coerces to but not vice-versa — `int4`→`int8`
→`numeric`. The common type is therefore the **widest member present**.

- `selectCaseCommonType(resultTypes)` (`resolver_expr.go`) now returns the widest
  of the family: `numeric` if any numeric appears, else `int8` if any `int8`
  appears, else `int4`. Any type outside `{int4,int8,numeric}` (a float, a
  collatable type, …) still returns false → SQL text.
- `coerceCaseResult(node, from, to)` gains the `int4`→`int8` arm: it wraps the
  `int4` result in `wrapInt4ToInt8Cast` — the implicit `int8(int4)` cast
  `FuncExpr` (`pg_cast.dat`: castsource int4 → casttarget int8, castfunc
  `int8(int4)` **OID 481**, castcontext `'i'`, castmethod `'f'`), funcresulttype
  int8, funcformat 2, byte-identical to PG's un-const-folded stored tree. The
  `int8` side never needs a cast.
- Rebuild: `rebuildFuncExprWith` (`rebuild.go`) recognises the new cast via
  `isImplicitInt4ToInt8Cast` (funcid 481 / funcformat 2 / funcresulttype int8 /
  one arg) alongside the sub-slice-4a `isImplicitIntToNumericCast`, and unwraps it
  to the inner integer literal — so a mixed CASE is a fixed point (`THEN 1 ELSE
  5000000000` → `int8(int4)(1)` / int8 → rebuilds to `THEN 1 ELSE 5000000000`).

### Gates (all green)

- `internal/pgnodes/case_test.go` — four live-captured PG18.3 scalar `adbin`
  goldens through the golden/codec/rebuild-fixed-point loops:
  `crossfam_int4_then_int8_else` (cast on `WHEN`), `crossfam_int8_then_int4_else`
  (cast on `ELSE`), `crossfam_simple_int4_int8` (simple form),
  `crossfam_two_int4_casts` (a multi-arm CASE with two `int8(int4)` casts).
  `TestCaseDegradesGracefully` swaps its now-canonical `int4`+`int8` case for
  `int4`+`float8` (common type `float8`, outside the modeled family → still SQL
  text) alongside the `text` case.
- `internal/pgnodes/datum.go` — added `OidFloat8 = 701` to express that boundary.
- full `pgnodes` package + `go vet ./internal/pgnodes/` + `go build ./...` clean;
  executor `TestCanonicalAttrdefText`/`TestDefault`/`TestResolveForColumn` +
  initdb `TestRebuildAttrdefExpr` green.

## Sub-slice 15 — `CASE` cross-FAMILY float result coercion (`float4`→`float8`)

Sub-slices 13/14 closed the exact-integer / numeric family. This slice adds the
second numeric family PG models: the **binary floats** `{float4,float8}`. A mixed
`CASE` whose results are `float4`+`float8` now folds to `float8` instead of
degrading to SQL text.

`select_common_type` (`parse_coerce.c`) walks the result list: `float8` **is** a
preferred type in the numeric category (`typispreferred=t`), so `float4` implicitly
coerces to `float8` but never the reverse — the common type of any float mix is
`float8`.

- `selectCaseCommonType(resultTypes)` (`resolver_expr.go`) is restructured to
  classify each result into one of two disjoint families and only fold a mix that
  stays **within one family**: the exact-integer family `{int4,int8,numeric}`
  (widest present) *or* the float family `{float4,float8}` (→ `float8`). A
  **cross-family** mix (e.g. `int4`+`float8`) — which PG itself folds to `float8`
  via an implicit int→float cast — is deliberately left unmodeled here and still
  degrades to SQL text (all-or-nothing).
- `coerceCaseResult(node, from, to)` gains the `float4`→`float8` arm: it wraps the
  `float4` result in `wrapFloat4ToFloat8Cast` — the implicit `float8(float4)` cast
  `FuncExpr` (`pg_cast.dat`: castsource float4 → casttarget float8, castfunc
  `float8(float4)` **OID 311** / `prosrc ftod`, castcontext `'i'`, castmethod
  `'f'`), funcresulttype float8, funcformat 2, byte-identical to PG's un-const-
  folded stored tree. The `float8` side never needs a cast.
- Rebuild: `rebuildFuncExprWith` (`rebuild.go`) recognises the new cast via
  `isImplicitFloat4ToFloat8Cast` (funcid 311 / funcformat 2 / funcresulttype
  float8 / one arg) alongside the sibling int casts, and unwraps it to the inner
  `float4` result — so a mixed float CASE is a fixed point.

Note: this canonicalizer has **no float *literal* leaf** (a decimal literal is
`numeric`, and `::float` casts are not yet a resolvable node), so the only way a
`float4`/`float8` result reaches the CASE walk today is a float-returning function
call — e.g. the `float4()`/`float8()` conversion functions (funcid 318/316,
funcformat 0). The three goldens use exactly that shape.

### Gates (all green)

- `internal/pgnodes/case_test.go` — three live-captured PG18.3 scalar `adbin`
  goldens (table `cf`) through the golden/codec/rebuild-fixed-point loops:
  `crossfam_float4_then_float8_else` (cast on `WHEN`),
  `crossfam_float8_then_float4_else` (cast on `ELSE`),
  `crossfam_two_float4_casts` (a multi-arm CASE with two `float8(float4)` casts).
- `internal/pgnodes/datum.go` — added `OidFloat4 = 700` + `float4`/`float8`
  `caseTypeMeta` entries (float8 is FLOAT8PASSBYVAL byval on the 64-bit build).
- full `pgnodes` package + `go vet ./internal/pgnodes/` + `go build ./...` clean;
  executor `TestCanonicalAttrdefText`/`TestDefault`/`TestResolveForColumn` +
  initdb `TestRebuildAttrdefExpr` green; gofmt clean.

## Sub-slice 16 — UNIFIED cross-FAMILY `CASE` coercion (any int/numeric/float → `float8`)

Sub-slices 13–15 folded a `CASE` mix only *within* one numeric family (the
exact-integer/numeric `{int4,int8,numeric}` OR the binary-float `{float4,float8}`).
This slice unifies them: PG's `select_common_type` (`parse_coerce.c`) walks the
**whole numeric type category** (`TYPCATEGORY_NUMERIC 'N'` =
`{int2,int4,int8,numeric,float4,float8,oid,…}`), and **`float8` is that category's
PREFERRED type** (`typispreferred=t`). So the moment any result is `float8`, the
walk stays on it — the common type of a mix that *contains* `float8` is `float8`,
and every other member coerces up to it. A `bool DEFAULT`/`float8 DEFAULT`
`CASE WHEN … THEN 1 ELSE float8(2) END` now folds to canonical `casetype 701`
instead of SQL text.

- `selectCaseCommonType(resultTypes)` (`resolver_expr.go`) is rewritten from the
  two-disjoint-families form to one category walk over
  `{int4,int8,numeric,float4,float8}`. Precedence of the reachable common types:
  `float8` (preferred — wins whenever present) > `numeric` > `int8` > `int4`. Any
  type outside the set (or a collatable type) → degrade.
- `coerceCaseResult(node, from, to)` gains the `int4`/`int8`/`numeric` → `float8`
  arms via new `wrapToFloat8Cast`, choosing the implicit cast `FuncExpr` by source
  type (`pg_cast.dat`, all castcontext `'i'`, castmethod `'f'`): **`float8(int4)`
  316**, **`float8(int8)` 482**, **`float8(numeric)` 1746**; funcresulttype
  `float8`, funcformat 2, byte-identical to PG's un-const-folded stored tree.
  (`float4`→`float8` still routes through `wrapFloat4ToFloat8Cast`/311.)
- Rebuild: `rebuildFuncExprWith` (`rebuild.go`) recognises the new casts via
  `isImplicitToFloat8Cast` (funcid ∈ {316,482,1746} / funcformat **2** /
  funcresulttype float8 / one arg) and unwraps them to the inner result — a fixed
  point. The `funcformat==2` guard is load-bearing: the same OIDs appear with
  funcformat **0** for an explicit `float8(<int>)` conversion call (which must
  rebuild back to that call, e.g. the ELSE `float8(2)` in `uc1`), and only the
  implicit-cast form is unwrapped.

**Scope boundary (deliberately unmodeled → degrade):** a mix with `float4` but
**no** `float8` (e.g. `numeric`+`float4`+`int4`) has PG common type `float4`, and
PG then wraps the *whole* `CASE` in an OUTER `float8(float4)` **column** cast
(observed live: `casetype 700` inside an outer `FUNCEXPR 311`). This subset has no
`int/numeric`→`float4` cast arm and emits no outer column cast, so
`selectCaseCommonType` returns false for a float4-common mix and the writer keeps
SQL text.

### Gates (all green)

- `internal/pgnodes/case_test.go` — four live-captured PG18.3 scalar `adbin`
  goldens (tables `ucf`/`ucf5`) through the golden/codec/rebuild-fixed-point loops:
  `unified_int4_to_float8_when` (int4→float8 on `WHEN`, native float8 ELSE),
  `unified_int8_to_float8_else` (int8→float8 on `ELSE`),
  `unified_numeric_to_float8_when` (numeric→float8 on `WHEN`),
  `unified_int4_float4_float8_three_families` (int4→float8 316 + float4→float8 311
  + native float8 in one CASE). `TestCaseDegradesGracefully` swaps its stale
  `int4_float8_no_numeric` case (now canonical) for `float4_common_no_float8`.
- full `pgnodes` package + `go vet ./internal/pgnodes/` + `go build ./...` clean;
  executor `TestCanonicalAttrdefText`/`TestDefault`/`TestResolveForColumn` +
  initdb `TestRebuildAttrdefExpr`/`TestRebuildViewFromEvAction` green; gofmt clean;
  pgbench smoke via pre-commit hook.

## Sub-slice 17 — simple-form `CASE` WHEN-value implicit coercion

Sub-slices 12–16 canonicalized the simple `CASE` **operand** (a `CaseTestExpr`
placeholder) and cross-type **result** coercion, but left the **WHEN-value**
side implicit: when the operand and a `WHEN` value differ in type, PG's
`transformCaseExpr` expands each arm to `makeSimpleA_Expr("=", placeholder,
value)` and runs it through `make_op`, which coerces the value up to the operand
type whenever no native cross-type `=` operator exists. For a **numeric operand**
there is no `numeric = int4` operator, so PG selects `numeric_eq` (opno 1752 /
opfuncid 1718) and wraps the `int4` value in the implicit `int4_numeric`
(funcid 1740, funcformat 2) cast; the `CaseTestExpr` placeholder stays un-coerced.

No resolver code changed — `resolveCaseWhenCond` already resolves the value with
the operand type as its *expected* type, so `resolveIntLiteral` (sub-slice 4a)
applies the identical `int4_numeric` cast and `buildOpExpr` then picks the exact
`numeric_eq` operator. This slice makes that intentional-but-previously-untested
path a **guaranteed** one: two byte-diff goldens (`sd.n1`/`sd.n2`) captured live
from PG18.3, plus two live-oracle cases so the divergence is caught against a real
server.

A pair PG would resolve through a *native* cross-type operator — e.g. an `int8`
operand + `int4` value picks `int8=int4` (opno 416) and leaves the value
**un-coerced** — was NOT modeled at this slice (`resolveCaseWhenCond` resolved the
value with the operand type as its expected type, which for an `int8` operand
coerced the `int4` value up to `int8` and emitted a divergent `int8=int8` tree).
**Sub-slice 18 (below) closes this**, resolving the value at its natural type and
selecting the native operator when one exists.

### Gates (all green)

- `internal/pgnodes/case_test.go` — two live-captured PG18.3 scalar `adbin`
  goldens (table `sd`) through the golden/codec/rebuild-fixed-point loops:
  `simple_numeric_operand_int_when_coerce` (`CASE 5.0 WHEN 1 THEN 100.0 ELSE
  200.0 END`) and `simple_numeric_operand_int_when_coerce_multi` (two WHEN arms).
- `internal/testport/oracle_pgnodes_adbin_test.go` — the same two cases
  (`case_simple_numeric_coerce{,_multi}`) added to the live oracle (27 cases total).
- full `pgnodes` package + `go vet ./internal/testport/` + `go build ./...` clean;
  gofmt clean; pgbench smoke via pre-commit hook.

## Sub-slice 18 — simple-form `CASE` WHEN-value NATIVE cross-type operator

Sub-slice 17 handled the *coercion* branch of `make_op` (numeric operand + int
value → cast + `numeric_eq`). This slice adds the *native-operator* branch that
runs FIRST in PG's `make_op` → `oper()`: when a `pg_operator` row matches the
operand and value types directly — including a cross-type integer operator —
PG uses it with **both arguments left un-coerced**. The two integer cross-type
`=` operators are `int8=int4` (opno 416 / oprcode `int84eq` 474) and its
commutator `int4=int8` (opno 15 / oprcode `int48eq` 852); PG's seed also carries
`int2=int4` (532), `int2=int8` (1862), etc.

### The two phases (`resolveCaseWhenCond`)

The old body resolved the WHEN value with the operand type as its *expected*
type — which for an `int8` operand + `int4` literal silently promoted the value
to `int8` and emitted `int8=int8` (opno 410), a tree PG never writes. The new
body models `make_op` faithfully:

1. Resolve the value at its **natural** type (`rec(when, 0)` — no coercion
   context), so a small integer literal stays `int4`, a big one `int8`, `2.5`
   `numeric`, etc.
2. `catalog.LookupOperatorForNode("=", operandType, valType)` — if a native row
   exists (exact or cross-type), use the value **un-coerced**; `buildOpExpr`
   fills the exact `opno`/`opfuncid`/`opresulttype`.
3. Otherwise (no native row, e.g. `numeric = int4`) fall to sub-slice 17's path:
   `coerceCaseResult(value, valType, operandType)` widens the value up to the
   operand type and `buildOpExpr` selects the same-type operator.

The `CaseTestExpr` placeholder (the operator's left arg) is never itself wrapped
in a coercion — the shape `ruleutils` deparses — so a pair that would require
coercing the *operand*, or a value coercion `coerceCaseResult` does not model,
still degrades to SQL text. The numeric-operand goldens from sub-slice 17 reach a
byte-identical tree under the new body (natural-type `int4` Const + `int4_numeric`
cast via `coerceCaseResult` == the old expected-type resolution), so no existing
golden changed.

### Gates (all green)

- `internal/pgnodes/case_test.go` — two live-captured PG18.3 scalar `adbin`
  goldens through the golden/codec/rebuild-fixed-point loops:
  `simple_int8_operand_int4_when_native`
  (`CASE 5000000000 WHEN 1 THEN 10000000000 ELSE 20000000000 END`, opno 416) and
  `simple_int4_operand_int8_when_native`
  (`CASE 1 WHEN 5000000000 THEN 10 ELSE 20 END`, opno 15).
- `internal/testport/oracle_pgnodes_adbin_test.go` — the same two cases
  (`case_simple_int8_operand_int4_when` / `case_simple_int4_operand_int8_when`)
  added to the live oracle (**29 cases total**, all byte-identical vs PG18.3).
- full `pgnodes` package + `TestOraclePgnodesEvActionBytesMatchPG` green;
  `go vet ./internal/pgnodes/ ./internal/testport/` + `go build ./...` clean;
  gofmt clean; pgbench smoke via pre-commit hook.

## Sub-slice 19 — explicit integer `::type` casts

Every `CASE`/`Const`/`FuncExpr` slice so far emitted the *implicit* casts PG's
`coerce_type` derives from context (int→numeric, int4→int8 widening, →float8).
This slice models the **explicit** `expr::type` cast for the integer family
(`int2`/`int4`/`int8`), which PG stores differently: `coerce_to_target_type`
called with `COERCION_EXPLICIT` builds a `FuncExpr` with `funcformat`
`COERCE_EXPLICIT_CAST` (**1**) naming the `pg_cast` conversion function — and,
unlike an implicit coercion, that cast node is **kept verbatim in `adbin`**
(ruleutils deparses it back to `::type`). A cast to the operand's *own* type is a
no-op: `coerce_type` returns the inner node unchanged, so `5::int4` stores a bare
`int4` `Const` and `9999999999::int8` (a literal `make_const` already typed
`int8`) a bare `int8` `Const` — no `RelabelType`, no `FuncExpr`.

### Forward (`resolver_expr.go`)

`resolve` gains a `*parser.CastExpr` arm → `resolveCastExpr`:

1. reject a typmod-qualified target (`::numeric(10,2)`) and a non-`pg_catalog`
   schema qualifier;
2. map the target type name via `catalog.TypeNameToOID`; only the integer family
   (`isIntegerType`) is modeled — anything else degrades to SQL text;
3. resolve the operand at its **natural** type (`expected=0`) so its own
   magnitude typing (`int4` vs `int8`) decides the *source* before the cast;
4. if source == target, return the inner node (the no-op case);
5. else look the conversion function up by (source, target) in
   `integerCastFuncid` — `int2(int4)=314`, `int8(int4)=481`, `int4(int8)=480`,
   `int2(int8)=714`, all from `pg_proc.dat`/`pg_cast.dat` — and build the
   `funcformat 1` `FuncExpr` (the sibling of `wrapInt4ToInt8Cast`'s `funcformat 2`
   form; same OID 481, different funcformat).

`operandTypmodCollid` also gains a `*FuncExpr` arm (`typmod -1`, `collid
funccollid`, matching PG's `exprTypmod(T_FuncExpr)==-1` / `exprCollation ==
funccollid`), so a **simple-form `CASE` whose operand is an explicit cast**
(`CASE 5::int8 WHEN 1 THEN … END`) types its `CaseTestExpr` placeholder from the
cast `FuncExpr` instead of degrading — closing the "explicit-cast operand simple
CASE" item.

### Reload (`rebuild.go`)

`rebuildFuncExprWith` gains an `explicitIntegerCastTypeName` branch (checked
*after* the implicit-cast unwrap arms, gated on `funcformat==1`): an explicit cast
rebuilds to a `*parser.CastExpr{Operand: inner, Type: {Name: "int2|int4|int8"}}`,
**not** the bare argument. Re-resolving `inner::type` re-emits the identical
`funcformat 1` `FuncExpr` — a fixed point — while the implicit `481`/`funcformat 2`
form still unwraps. goopg evaluates the rebuilt `CastExpr` natively, so its own
reload value is unchanged; the win is that a PG standby can now EVALUATE the
stored `adbin`.

### Gates (all green)

- `internal/pgnodes/cast_test.go` — 7 live-captured PG18.3 scalar `adbin` goldens
  (`5::int2`/`5::int8`/`9999999999::int4`/`9999999999::int2` explicit casts,
  `5::int4`/`9999999999::int8` no-ops, and the explicit-cast-operand simple CASE)
  through golden + codec-round-trip + resolve→Rebuild→re-resolve fixed-point
  loops, plus a degradation matrix (non-integer target, numeric/text source,
  typmod-qualified).
- `internal/testport/oracle_pgnodes_adbin_test.go` — the same 7 cases added to the
  live oracle (**36 cases total**, all byte-identical vs PG18.3).
- full `pgnodes` package + `TestOraclePgnodesAdbinBytesMatchPG` +
  `TestE2E_FailoverGoopgToPG` green; initdb/executor attrdef sibling tests green;
  `go vet` + `go build ./...` clean; gofmt clean; pgbench smoke via pre-commit.

## Sub-slice 20 — explicit numeric↔integer `::type` casts

Sub-slice 19 modeled the explicit integer→integer casts; this slice extends the
same funcformat-1 machinery to the rest of the **numeric family** — casts that
cross the integer/`numeric` boundary in either direction (`5.5::int4`,
`5::numeric`, `(-2.5)::int4`). PG stores each identically to the integer casts: a
`COERCE_EXPLICIT_CAST` (`funcformat` **1**) `FuncExpr` naming the `pg_cast`
conversion function, kept verbatim in `adbin`, with the operand resolved at its
**natural** type first (a decimal literal → `numeric` `Const`, an integer literal
→ `int4`/`int8` `Const`).

### Changes

- `resolver_expr.go`: `isIntegerType`→`isNumericFamilyType` (adds `numeric` to the
  accepted target set) and `integerCastFuncid`→`numericFamilyCastFuncid` (adds the
  six cross-boundary arms: `int2/int4/int8_numeric`=1782/1740/1781 int→numeric;
  `numeric_int2/int4/int8`=1783/1744/1779 numeric→int). The typmod-qualified guard
  (`len(v.Typmods)!=0`) still rejects `::numeric(10,2)` — that length coercion is a
  distinct `numeric(numeric,int4)` call, not modeled here.
- `rebuild.go`: `explicitIntegerCastTypeName`→`explicitCastTypeName` gains the
  numeric arms → target type name (`1740/1781/1782`→`numeric`, `1744`→`int4`,
  `1779`→`int8`, `1783`→`int2`). The `funcformat==1` guard stays load-bearing:
  `1740`/`1781` also appear as the **implicit** int→numeric coercion (funcformat 2,
  sub-slice 4a), which rebuilds by unwrapping instead. `rebuildConst`'s existing
  `numeric` arm (sub-slice 3) reconstructs a `numeric` operand (incl. the negative
  `(-2.5)` fold) for a fixed point.

### Gates (all green)

- `internal/pgnodes/cast_test.go` — 6 new live-captured PG18.3 scalar `adbin`
  goldens (`5.5::int4`/`::int8`/`::int2`, `(-2.5)::int4`, `5::numeric`,
  `9999999999::numeric`) through the golden + codec + rebuild fixed-point loops;
  the degradation matrix swaps the now-canonical `numeric→int4` case for
  `numeric→float8` (float family still out of scope).
- `internal/testport/oracle_pgnodes_adbin_test.go` — the same 6 cases added to the
  live oracle (**42 cases total**, all byte-identical vs PG18.3, ≈1.45s).
- full `pgnodes` package + `TestE2E_FailoverGoopgToPG` + initdb/executor attrdef
  siblings green; `go vet` + `go build ./...` + gofmt clean; pgbench smoke via
  pre-commit.

## Sub-slice 21 — explicit float-family `::type` casts

Sub-slices 19–20 modeled the explicit `::type` casts within the integer/`numeric`
core; this slice extends the same funcformat-1 machinery across the **binary-float
boundary** (`float4`/`float8`, OIDs 700/701). All six types (`int2`/`int4`/`int8`/
`numeric`/`float4`/`float8`) are members of PG's `TYPCATEGORY_NUMERIC`, and every
ordered pair among them has a `pg_cast` conversion function (`castmethod 'f'`), so
any `expr::T` between them is a `COERCE_EXPLICIT_CAST` (`funcformat` **1**)
`FuncExpr` kept verbatim in `adbin`. Reachable user-facing forms — the operand is a
literal, which types naturally as `int4`/`int8`/`numeric` (there is **no** float
literal leaf) — are `5::float4`, `5::float8`, `5.5::float8`, `9999999999::float4`,
etc. A float **source** arm is reachable only through a nested `(x::float8)::int4`.

### Changes

- `resolver_expr.go`: `isNumericFamilyType` accepts `float4`/`float8` targets, and
  `numericFamilyCastFuncid` gains the full float matrix — int→float4
  (`float4(int2/int4/int8)`=236/318/652), int→float8 (`float8(int2/int4/int8)`=
  235/316/482), numeric↔float (`numeric_float4`=1745, `numeric_float8`=1746,
  `float4_numeric`=1742, `float8_numeric`=1743), float↔float (`float8(float4)`=311,
  `float4(float8)`=312), and float→int (`int2/int4/int8(float4)`=238/319/653,
  `int2/int4/int8(float8)`=237/317/483). No new node/codec — the existing `FuncExpr`
  path carries them.
- `rebuild.go`: `explicitCastTypeName` gains the float arms → target type name. The
  `funcformat==1` guard is **load-bearing**: `float8(float4)`=311 and the int/numeric
  →float8 OIDs (316/482/1746) also appear with funcformat 2 as the IMPLICIT `CASE`
  →float8 coercion (`isImplicitToFloat8Cast` / `isImplicitFloat4ToFloat8Cast`,
  sub-slices 15–16), which rebuild by unwrapping instead. Rebuild needs no float
  `Const` handling — a float source is always another (cast) `FuncExpr`, never a
  bare float `Const`.

### Gates (all green)

- `internal/pgnodes/cast_test.go` — 7 new live-captured PG18.3 scalar `adbin`
  goldens (`5::float4`/`::float8`, `9999999999::float4`/`::float8`, `5.5::float4`/
  `::float8`, and the nested `(5.5::float8)::int4`) through the golden + codec +
  rebuild fixed-point loops; the degradation matrix swaps the now-canonical
  `numeric→float8` case for a `text→float8` source (unmodeled source arm).
- `internal/testport/oracle_pgnodes_adbin_test.go` — the same 7 cases added to the
  live oracle (**49 cases total**, all byte-identical vs PG18.3, ≈1.52s).
- full `pgnodes` package + `go vet` + gofmt clean; `TestE2E_FailoverGoopgToPG` +
  initdb/executor attrdef siblings green; pgbench smoke via pre-commit.

## Sub-slice 22 — explicit typmod-qualified numeric cast `::numeric(p,s)`

Sub-slices 19–21 modeled the *typmod-less* explicit `::type` casts. This slice adds
the first **length-coercion** cast: `expr::numeric(p[,s])`. PG's
`coerce_to_target_type` (parse_coerce.c) resolves this in two stages — `coerce_type`
coerces the operand to bare `numeric`, then `coerce_type_typmod` applies a length
coercion `numeric(numeric, int4)` (`funcid` **1703**) whose second argument is an
`int4` `Const` holding the packed `atttypmod`. The stored `adbin` (captured LIVE
from PG18.3) is:

```
FuncExpr 1703  (funcformat 1, EXPLICIT)
  ├─ arg0: operand→numeric — int operand wraps in int4_numeric/int8_numeric
  │        (1740/1781, funcformat 2 IMPLICIT); a decimal operand is a bare numeric Const
  └─ arg1: Const int4 = numerictypmodin(p,s) = ((p<<16)|(s&0x7ff)) + VARHDRSZ(4)
```

`numeric(10,2)` → 655366 `[6 0 10 0]`; `numeric(10,0)`/`numeric(10)` → 655364
`[4 0 10 0]` (scale defaults to 0). `numeric(10)` and `numeric(10,0)` pack to the
**same** typmod, so rebuild always emits the 2-element `[p,s]` form — a fixed point
either way.

### The RelabelType / column-typmod subtlety (why the writer now threads typmod)

This bare-1703 shape is what PG stores **only when the column's own typmod equals
the cast's** (e.g. `col numeric(10,2) DEFAULT (5::numeric(10,2))`). When the column
is bare `numeric` (typmod −1), PG wraps the whole coercion in a `RelabelType` that
re-labels the result back to the column's typmod — a node this slice does **not**
model. `ResolveForColumn` only knew the column's *type OID*, not its typmod, so it
could not tell the two apart. The fix adds `ResolveForColumnTypmod(e, targetType,
targetTypmod)` (old signature delegates with typmod −1); a top-level 1703 length
coercion whose embedded typmod ≠ `targetTypmod` degrades to SQL text. The executor
writer (`sys_pg_attrdef.go`) derives the column typmod via
`pgnodes.NumericColumnTypmod(col.Type.Args)` — so a matching `numeric(p,s)` column
emits canonical bytes and a bare `numeric` column degrades. Nested/intermediate
typmod casts (`(5::numeric(10,2))::int4`) are unaffected: only the **top** node is
column-coerced, so the check inspects only the top node.

### Changes

- `resolver_expr.go`: `resolveCastExpr` routes a typmod-qualified `numeric` target to
  new `resolveNumericTypmodCast`, which coerces the operand to `numeric` (via
  `wrapIntToNumericCast`, funcformat 2) and wraps it in the 1703 length coercion
  (funcformat 1). `numericTypmodValue` reproduces `numerictypmodin`. New
  `ResolveForColumnTypmod` + `NumericColumnTypmod`.
- `rebuild.go`: `numericCastPackedTypmod` recognizes the 1703/funcformat-1 node and
  returns its packed typmod (used by the writer gate); `numericTypmodCastPS` decodes
  it back to `(p,s)`; `rebuildFuncExprWith` rebuilds it to a `CastExpr{Type:numeric,
  Typmods:[p,s]}` (fixed point, placed before `explicitCastTypeName`).
- `sys_pg_attrdef.go`: writer threads `col.Type.Args`→typmod into
  `ResolveForColumnTypmod`.

Sources other than `int4`/`int8`/`numeric` (int2, the binary floats) reach `numeric`
via a different helper set (`int2_numeric`=1782, `float*_numeric`=1742/1743) not
wired here and degrade; a scale outside `0 ≤ s ≤ p` also degrades (conservative
subset of `numerictypmodin`'s range).

### Gates (all green)

- `internal/pgnodes/cast_test.go` — 3 new live-captured PG18.3 goldens
  (`5::numeric(10,2)`, `5.5::numeric(10,2)`, `5::numeric(10,0)`) through the golden +
  codec + rebuild fixed-point loops; the golden struct gains a `colTypmod` field and
  the acceptance check uses `ResolveForColumnTypmod`. The degradation matrix reframes
  the old `typmod_qualified` case as `typmod_cast_bare_numeric_col` (bare-numeric
  column → RelabelType, not modeled).
- `internal/testport/oracle_pgnodes_adbin_test.go` — 3 cases added to the live oracle
  with `numeric(p,s)` columns (**52 cases total**, all byte-identical vs PG18.3,
  ≈1.57s); a `numericColSQLTypmod` helper mirrors the writer's typmod derivation.
- full `pgnodes` package + `go vet` + gofmt clean; initdb reload + executor attrdef
  siblings green; pgbench smoke via pre-commit.

## Byte-diff oracle gate (adbin) — `internal/testport/oracle_pgnodes_adbin_test.go`

The per-datum golden tests above each hard-code a `want` `adbin` string a
developer captured by hand from a live PG18.3. That is fast but leaves two gaps:
a hand-copied golden can silently drift from what PG18 actually emits, and a new
datum/expression type needs a separate manual capture step. `TestOraclePgnodes\
AdbinBytesMatchPG` closes both by re-deriving the oracle **live**: for each
`(column type, DEFAULT expr)` case it

1. `CREATE TABLE t (v <type> DEFAULT (<expr>))` on a fresh PG18 (`pgcluster.New`
   + `Start`),
2. reads back `pg_attrdef.adbin::text`,
3. normalizes `:location <N>` → `:location -1` (PG18 stores catalog
   `pg_node_tree` with `write_location_fields=false`, so for `adbin` this is a
   no-op belt; goopg's `Out` always emits `-1`), and
4. asserts `pgnodes.ResolveForColumn(parser.ParseExpr(expr), oid)` → `Out` is
   **byte-identical**. A goopg SQL-text fallback (`ok==false`) on a case PG stores
   canonically is itself a failure.

The 27 cases span every S4-canonical family: bare `Const` leaves (int4/int8 by
magnitude, folded negative, text, numeric decimal/scientific/negative),
`int4→numeric` cast FuncExpr, built-in FuncExpr (`upper`), timestamptz literal
Const, `BoolExpr`/`NullTest`/`OpExpr` (incl. `makeAndExpr` 3-arg flattening),
`BooleanTest` (`IS TRUE`/`IS UNKNOWN`), `DistinctExpr` (int + text), and
`CaseExpr` (searched + simple, same-type and cross-type int→numeric / int4→int8
result coercion, and simple-form WHEN-value int→numeric coercion). Every case is
drawn from an existing `internal/pgnodes` golden
so `ResolveForColumn` is known to accept it — the added value is that the `want`
now comes from a live PG18, catching both transcription drift and future
coverage gaps automatically.

Gating matches the other heterogeneous E2E tests: skipped under `-short`, when
`GOOPG_SKIP_PGNODES_ORACLE` is set, and when the upstream PG binaries are absent
(`pgcluster.Available`). Wall time ≈ 1.3 s (one initdb + 25 `CREATE TABLE`s).

## Byte-diff oracle gate (ev_action) — `internal/testport/oracle_pgnodes_ev_action_test.go`

The query-tree analogue of the `adbin` oracle above, closing the same
transcription-drift / coverage-drift gaps for the **view `ev_action`
(`pg_rewrite`) path**. `TestOraclePgnodesEvActionBytesMatchPG` seeds one shared
base relation `bench_log(client int, src text)` on a fresh PG18, then for each
canonical view case

1. `CREATE VIEW v AS <select>` on the live PG18,
2. reads back `pg_rewrite.ev_action::text` (the single `_RETURN` rule),
3. normalizes `:location <N>` → `:location -1` (here the belt actually matters —
   a stored rule can carry real source offsets that goopg's `Out` always writes
   as `-1`), and
4. asserts `pgnodes.ResolveViewQuery(sel, resolver)` → `OutRuleAction` is
   **byte-identical**. A goopg `ErrUnsupported` degradation on a case PG stores
   canonically is itself a failure.

The one piece the `adbin` path does not need is the **`RelationResolver` shim**:
`ResolveViewQuery` must stamp each `Var`'s `varno`/`varattno`/`vartype`/
`varcollid` and the RTE `relid` from the base relation, so the test implements
`pgnodes.RelationResolver` (`liveRelationResolver`) by reading the SAME live
cluster's `pg_class` (real `oid`/`relkind`) and `pg_attribute` (name, `attnum`,
`atttypid`, `atttypmod`, `attcollation`) via `string_agg` + `QueryScalar`.
Because the relid and column OIDs are read **live**, goopg's emitted bytes and
PG's `ev_action` reference the identical relid — nothing bakes in a fixed 16384,
so the diff is robust to catalog OID drift.

The 13 cases mirror the `internal/pgnodes` view goldens (`resolver_query_test.go`
`v`/`v2`, `view_bool_null_test.go` `v3`–`v13`) and exercise every canonical
query-qual shape S4 emits: `OpExpr`, a computed `FuncExpr` target (`upper`),
`BoolExpr` AND/OR/NOT, `NullTest`, `BooleanTest`, `CaseExpr` (searched + simple),
and `DistinctExpr` (incl. its `NullTest` rewrite against a NULL operand). Same
gating and ≈ 1.3 s wall time as the `adbin` oracle.

## Deferred

- **Cross-family** float-common-*without*-float8 `CASE` mixes still degrade: a mix
  whose PG common type is `float4` (float4 present, no float8) resolves to a
  `float4` CASE wrapped in an outer `float8(float4)` column cast, which needs the
  `int/numeric`→`float4` cast arms plus outer-column-cast modeling; and the
  date-time families are not yet modeled. Sub-slice 16 covers every mix whose
  common type is `int8`/`numeric`/`float8`.
- Simple-form `WHEN`-value resolution now models both `make_op` phases — the
  native cross-type operator (sub-slice 18) and the coerce-value-up path
  (sub-slice 17, limited to the `int4/int8`→`numeric` widenings `coerceCaseResult`
  emits). A `WHEN` value whose coercion to the operand type is NOT one of those
  arms (e.g. a text/date operand with a differently-typed value, or a coercion PG
  represents with a `RelabelType` above the placeholder) still degrades to SQL
  text. An explicit-cast operand (`5::int8 WHEN …`) also still degrades: PG writes
  it as an un-const-folded `int8(int4)` `FuncExpr` operand (funcformat 1), which
  `operandTypmodCollid` rejects.
- The byte-diff oracle now covers **both** the **`adbin` (column DEFAULT) path**
  and the **view `ev_action` (`pg_rewrite`) path** (see the two oracle sections
  above); the view oracle's `liveRelationResolver` answers base-table column
  metadata from the live PG18 catalog.
- Operator-driven implicit coercion in view quals (which would let a
  `timestamptz`/`int2`→`numeric` string literal resolve inside a view `WHERE`)
  is also still SQL-text — only the exact scalar-column DEFAULT context folds a
  `timestamptz`/numeric literal.
- **`::numeric(p,s)` on a typmod-mismatched column** (sub-slice 22): only a column
  whose typmod *equals* the cast's emits the canonical bare-1703 form. A bare
  `numeric` column (typmod −1) — or any mismatched `numeric(p',s')` — degrades,
  because PG wraps the length coercion in a `RelabelType` re-labeling to the column
  typmod (`postgres/src/backend/parser/parse_coerce.c:coerce_type_typmod` →
  `coerce_to_target_type`'s trailing `RelabelType`), a node not yet in the IR. Also
  still deferred: typmod-qualified casts to the **other** length types (`varchar(N)`
  = `CoerceViaIO`, `timestamp(N)`, `bit(N)`), and non-int/numeric sources into
  `numeric(p,s)` (`int2`, the binary floats).
