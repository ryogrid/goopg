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

## Deferred

- View-query `WHERE`/target `BoolExpr`/`NullTest`: `resolver_query.go` resolves
  quals in its own `Var`-aware scope and does not yet route through these
  helpers — next sub-slice (extract `*With` recursion variants, mirroring how
  `rebuildOpExpr`/`rebuildFuncExpr` were made recursion-injectable in 0123-0004
  sub-slice 2b).
- `numeric`/`timestamptz` datums, `CASE`, `BooleanTest` (`IS TRUE`/`IS FALSE`),
  `IS DISTINCT FROM`, and the byte-diff oracle gate remain in M0123-S4.
