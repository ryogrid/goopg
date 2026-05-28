# M0097-0042: limit / union / returning regress improvements

**Milestone:** M0097-0042  
**Date:** 2026-05-27  
**Tests affected:** `limit`, `union`, `returning`  
**Baseline change:** limit 551 → 47, union 1097 → 48, returning 732 → 475

## Summary of changes

### 1. OFFSET after FETCH FIRST (`internal/parser/select.go`)

SQL standard §7.12 allows `FETCH FIRST n ROWS WITH TIES OFFSET k` where OFFSET
follows the FETCH clause. Added a second `acceptKeyword(KwOffset)` check after
the FETCH FIRST block in `parseSelect`.

### 2. WITH TIES full implementation

Prior code handled `FETCH FIRST n ROWS WITH TIES` by recording `WithTies=true`
in the planner `Limit` node but lacked executor support. Complete implementation:

- **Planner (`plan.go`):** Added `WithTies bool` and `TiesKeys []Expr` to
  `Limit` node. `wrapSetOpSortLimit` collects ORDER BY key expressions into
  `TiesKeys` when `WithTies=true`.
- **Executor slab (`opnode.go`):** `limitState` gains `tieKeyExprIdxs []int32`,
  `tieKeyVals Row`, `withTies bool`, and `inTiesPhase bool`. `inTiesPhase` is
  set once `emitted >= limitCount`; subsequent rows are emitted only while their
  tie-key values equal `tieKeyVals`.
- **Executor operator (`executor.go`):** builds `tieKeyExprIdxs` from
  `p.TiesKeys` at construction time; `opOpen` resets `inTiesPhase` and
  `tieKeyVals` on each Open call.
- **NULL LIMIT with WITH TIES**: returns `ERROR: row count cannot be null in
  FETCH FIRST ... WITH TIES clause` (code 22004), matching PostgreSQL.

### 3. exprType additions for return-type inference (`internal/planner/planner.go`)

`exprType(*FuncCall)` lacked entries for several functions; the missing types
caused `typeOIDFor("unknown") = 25 (text)` in the wire protocol RowDescription,
making psql left-align numeric columns. Added:

| Function | Return type |
|----------|-------------|
| `nextval`, `currval`, `lastval`, `setval` | `int8` |
| `random`, `random_normal`, `drandom` | `float8` |
| `generate_series` | `int8` |

### 4. Float8 arithmetic with KindString datums (`internal/executor/expr.go`)

`random()` returns `NewStringDatum("0.5")` (KindString). Two arithmetic paths
needed updating:

**Fast path** (`evalExprSlot`, `BinaryOp` with `ResultType == "float8"`):
Added `else if left.Kind == KindString { lf, _ = strconv.ParseFloat(left.StringValue(), 64) }`
analogous to the existing KindNumeric branch.

**Slow path** (`evalBinary`, `OpMul`/`OpDiv`/`OpMod`):
The `OpAdd`/`OpSub` branch already had string-to-numeric conversion via
`parseNumeric`; added the same conversion to the `OpMul`/`OpDiv`/`OpMod` block.

### 5. evalCastTyped / roundFloatToInt KindString fixes (`internal/executor/expr.go`)

After adding `random() → float8` to `exprType`, expressions like `(random()*.1)::int`
now go through the float8 fast path and produce `KindString{"0.05"}`. Two fixes:

- `evalCastTyped`: condition `isFloatSourceType(sourceType) && d.Kind == KindNumeric`
  extended to also accept `d.Kind == KindString`.
- `roundFloatToInt`: reads `d.StringValue()` (instead of `numericText(d)`) when
  `d.Kind == KindString`.

### 6. resolveColumnRefAt schema-based fallback (`internal/planner/planner.go`)

`wrapSetOpSortLimit` creates `newResolveContext(nil, out)` — schema but no
bindings. `resolveColumnRefAt` returned "not found" immediately when
`len(ctx.bindings) == 0`, so `ORDER BY q2,q1` after `EXCEPT` raised
`column "q2" does not exist`.

Fix: when `len(ctx.bindings) == 0` and the column reference is unqualified
(`x.Table == ""`), scan `ctx.schema` directly and emit a `ColumnRef` into the
set-op output schema. This correctly resolves named columns in set-op ORDER BY.

### 7. FOR UPDATE / NO KEY UPDATE in set-ops (`internal/analyzer/analyzer.go`)

Added a check in `analyzeSelectWithParent` that rejects `FOR UPDATE` / `FOR NO
KEY UPDATE` / `FOR SHARE` / `FOR KEY SHARE` on either branch of a set-op,
returning SQLSTATE 0A000.

### 8. Sequence session state (`internal/server/dispatch.go`, `conn_tx.go`)

`currval` and `lastval` are session-scoped in PostgreSQL — they persist across
statements in the same connection. Implemented via new fields in `connTx`:

- `SeqCurrVals map[string]int64` — per-sequence last-nextval-called value.
- `SeqLastVal int64`, `SeqLastSet bool` — for `lastval()`.

`dispatchSimpleQueryViaExecutor` copies them to/from `ectx.CurrSeqVals /
LastSeqVal / LastSeqSet` around each query.

### 9. Cursor position tracking (`internal/server/dispatch.go`, `conn_tx.go`)

`cursorEntry` replaced the raw `SQL string` stored in `cursorMap`. The new
struct holds:

```go
type cursorEntry struct {
    SQL         string
    Rows        []executor.Row  // materialised result set (nil until first FETCH)
    Schema      planner.Schema
    Pos         int64           // current fetch position (0-based)
    Materialized bool
}
```

`executeFetch` materialises the cursor on first access and then advances/retreats
`Pos` for `FETCH FORWARD n` / `FETCH BACKWARD n` / `FETCH ALL` / `FETCH ABSOLUTE`.

### 10. ArrayConstructorExpr (`internal/parser/expr.go`)

Added `ArrayConstructorExpr{Elements []Expr}` to support `ARRAY[e1, e2, …]`
syntax encountered in union.sql. The executor evaluates it to a text
representation matching PostgreSQL's `{e1,e2,…}` format.

### 11. SELECT with empty target list before set-op (`internal/parser/select.go`)

`SELECT UNION SELECT` (empty FROM + empty target list) is valid when followed
immediately by UNION/INTERSECT/EXCEPT. Added a gate in `parseSelect`:
```go
isSetOpOrClause := p.cur().Kind == TokenKeyword && (
    p.cur().Keyword == KwUnion || KwIntersect || KwExcept || ...)
```
to skip target-list parsing when the next token is a set-op keyword.

### 12. orderBySubstitution star-expression guard (`internal/analyzer/analyzer.go`)

In a set-op context, `SELECT * … ORDER BY 1` has the ORDER BY at the set-op
level; `targets[0]` is an unresolved `*parser.StarExpr`. Substituting a star
expression into `analyzeExpr` caused a panic. Added a guard:
```go
if _, isStar := targets[idx].Expr.(*parser.StarExpr); !isStar {
    return targets[idx].Expr
}
```

### 13. Sequence ascending/descending default start value (`internal/executor/operators_ddl.go`)

`CREATE SEQUENCE` without explicit `START` now uses `start = 1` for ascending
sequences and `start = -1` for descending, matching PostgreSQL convention.

### 14. rowKey numeric normalisation (`internal/executor/operators_recursive_cte.go`)

`rowKey` strips trailing zeros after the decimal point so that `"0.0"`, `"0.00"`,
and `"0"` all produce the key `"0"`. Required for set-op deduplication when both
sides compute the same logical value with different numeric kinds
(`KindNumeric` vs `KindInt`).

## Remaining diff lines

| Test      | Lines | Primary blocker |
|-----------|-------|----------------|
| `limit`   | 47    | ProjectSet / SRF expansion in SELECT list |
| `union`   | 48    | Unordered result ordering (8+6+6), generate_series SRF (2+6), PL/pgSQL (4), parenthesized set-op ORDER BY scope (6) |
| `returning` | 475 | Table inheritance (INHERITS), UPDATE FROM, RETURNING OLD/NEW (PG17) |
