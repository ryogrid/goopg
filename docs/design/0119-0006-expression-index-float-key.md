# Float expression index keys: type-directed encoding (M0119-0006)

Status: accepted (2026-08-10)
Milestone: M0119-0006 (pg_amcheck server tier) — tenth slice
Related: [0119-0006d expression-index result type](0119-0006-expression-index-result-type.md),
[0119-0006c expression-index bulk build](0119-0006-expression-index-bulk-build.md)

## The defect

`encodeArbiterExprKey` is the single encoder behind all three expression-key
paths — the ON CONFLICT arbiter probe (`encodeArbiterKey`), the runtime index
maintain path (`encodeExprIndexKey`) and the CREATE INDEX / REINDEX bulk build
(`encodeCompositeBTreeKeyWithExprs`). It dispatched *purely on the runtime Datum
kind*, because an expression key column has no catalog column to read a type off
(`catalog.Index.Columns[i] == ""`).

Kind dispatch is sound only while an expression yields a stable kind across
rows. **Float breaks that invariant**, and it breaks it inside a single column:

goopg has no `KindFloat`. `internal/executor/codec.go` decodes a stored
float4/float8 by rendering it with `PGFloatOut` and re-parsing the text
(`floatTextDatum`). A value that prints as a plain decimal comes back
`KindNumeric`; one that prints in exponent form or as `Infinity`/`NaN` comes back
`KindString`. So for one `float8` column:

| stored value | printed | Datum kind | key encoder that fired |
|---|---|---|---|
| `1.5` | `1.5` | `KindNumeric` | `btree.EncodeNumericKey` |
| `1e+30` | `1e+30` | `KindString` | `btree.EncodeVarchar` |
| `Infinity` | `Infinity` | `KindString` | `btree.EncodeVarchar` |

Two different encoders, whose byte spaces do not interleave, writing into the
same B-tree. The index was therefore **not ordered at all**. Observed directly
by disabling the fix and dumping the built tree for
`CREATE INDEX ON t ((f * 2))` over rows `1.5, -2.25, 1e+30`:

```
02800000003300   (7 bytes — EncodeNumericKey of 3.0)
2d342e3500       (5 bytes — EncodeVarchar of "-4.5")
32652b333000     (6 bytes — EncodeVarchar of "2e+30")
```

Consequences: a range scan over such an index can miss arbitrarily many live
rows; `bt_index_check` sees a key space in no consistent order; and the
composite-key walk added in the 9th slice cannot consume a column whose width
varies per row. This is why the 9th slice had `exprKeyDecodeType` decline float
outright — the bytes genuinely were not invertible, because there was no single
encoding to invert.

## The fix

Type-directed, not kind-directed. `encodeArbiterExprKey` now takes the resolved
key expression and, when `planner.ExprResultType` says the expression's static
result type is a float type, encodes **every** row through
`btree.EncodeFloat8` — the same encoder a float8 *column* key already uses
(`encodeBTreeKeyForColumn`'s `isFloat8Type` arm). That encoder's sign/bit flip
reproduces PG's float8 opclass order, including `-Infinity < finite < Infinity <
NaN` (`float8_cmp_internal`, `src/backend/utils/adt/float.c`).

Seams:

- `internal/executor/operators_upsert.go`
  - `encodeArbiterExprKey(v Datum, keyExpr planner.Expr, pos int)` — new
    parameter; `keyExpr == nil` keeps the pre-existing kind dispatch verbatim, so
    a caller with no resolved expression is never made worse.
  - `exprKeyIsFloat(keyExpr)` — `planner.ExprResultType` + `isFloat8Type`.
    Resolution failure means "not known to be float", i.e. kind dispatch stays in
    charge.
  - `exprKeyDecodeType` gains the `float4`/`float8`/`real`/`double precision`
    arm: surrogate `float8`, `allowRoutine = true`. Routine-safe because the
    decode is literally the float8 *column* decode — a comparator declared over
    float8 already sees the `'g'`-formatted string `decodeIndexKeyColumn`
    returns for a float8 column key.
- `internal/executor/operators_ddl.go`
  - `datumToFloat64ForKey(v)` extracted from `encodeBTreeKeyForColumn`'s float
    arm and shared with the new expression arm, so the column path and the
    expression path coerce identically by construction (Hard-won Rule #2). It
    keeps the two distinct error classes the column path reports: `22003` for an
    unparseable float text, `42804` for a kind that is not a float at all.
- The three call sites each already hold the resolved expression
  (`oc.ArbiterExprs[i]`, `keyExprs[i]`, `planExpr`), so no plumbing was needed.

`exprKeyDecodeType` accepting float also un-declines amcheck's opclass
comparator for every index with a float expression key column: such an index
previously fell back to whole-key byte order and could not report
`item order invariant violated` at all.

## Gates

- `TestEncodeArbiterExprKeyFloatIsTypeDirected` — ten values spanning both Datum
  kinds and both infinities plus `NaN`, asserting every key is 8 bytes and
  strictly ascending in PG's float order. Ends with an explicit non-vacuity
  assertion: under `keyExpr == nil` the two representations must still produce
  differently-sized keys, or the test proves nothing.
- `TestExpressionIndexBuildFloatKey` — end-to-end across the bulk build and the
  runtime maintain path (`CREATE INDEX ON t ((f * 2))` over mixed-representation
  rows, then a post-build INSERT of `-1e+30`), scanning the physical tree and
  asserting 8-byte float-decodable keys in ascending float order.
- `TestExprKeyDecodeTypeRoundTrip` gains three float rows (numeric-kind,
  string-kind, `Infinity`); `…DeclinesUninvertible` drops float from its decline
  list; `…RoutineSafety` pins `allowRoutine = true` for float.

All confirmed non-vacuous by forcing `exprKeyIsFloat` to return false: the three
new/updated tests fail with the mixed-encoding key dump quoted above.

## Still deferred

- **Enum expression keys.** `encodeArbiterExprKey`'s `KindEnum` arm writes
  `EncodeFloat8(sort order)`, which needs the enum catalog entry to invert back
  to a label, so `exprKeyDecodeType` still declines. It is unreachable in
  practice today anyway: `planner.exactTypeOID` refuses enum names (they fall
  back to text(25)), so `ExprResultType` never resolves an enum. Closing it means
  teaching `ExprResultType` about user enum types via the catalog.
- **`interval` / `box` / `int4range` / array expression keys** — no encoder arm
  at all; rows with such a key are silently not indexed.
- **float4 precision.** A `float4` expression key is widened to float8 for the
  key, matching what goopg's float4 *column* keys already do. Upstream stores a
  4-byte key under `float4_ops`. Order is preserved either way, so this is a
  key-size divergence, not an ordering one.
