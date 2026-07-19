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

## Deferred

- `timestamptz` datums, `CASE`, `BooleanTest` (`IS TRUE`/`IS FALSE`),
  `IS DISTINCT FROM`, and the byte-diff oracle gate remain in M0123-S4. (The
  `int2`→`numeric` and general operator-driven implicit coercion in view quals
  are also still SQL-text — only the exact numeric-column DEFAULT context casts.)
