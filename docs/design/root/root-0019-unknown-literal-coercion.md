# root-0019 — Unknown-literal coercion (`int_col = '1'`)

## Problem

goopg hard-typed every bare string literal as `text` during analysis
(`internal/analyzer/analyzer.go`, `analyzeExpr` → `*parser.StringConst`). A
comparison between a numeric column and a quoted literal therefore failed the
analyzer's operand-compatibility check:

```
SELECT * FROM wp_users WHERE ID = '1';
ERROR:  operator = has incompatible operand types "bigserial" and "text"  (42804)
```

Real clients hit this constantly — WordPress issues `WHERE ID = '1'` /
`WHERE user_id = '1'` for every user/capability lookup, so the wp-admin
dashboard 403'd against goopg while the unquoted form worked fine.

## Upstream semantics

PostgreSQL types an undecorated string literal as the pseudo-type **`unknown`**
(`UNKNOWNOID`) and resolves it against context during operator/function
resolution:

- `postgres/src/backend/parser/parse_oper.c` — `oper()` /
  `oper_select_candidate()` resolve an `unknown` operand to the other operand's
  type.
- `postgres/src/backend/parser/parse_coerce.c` — `coerce_type()` applies the
  actual coercion (for a literal, by re-parsing the source text with the target
  type's input function; malformed text raises `22P02`).

Only the *literal* coerces. A genuine `text` column compared to an integer still
errors — which is why "type literals as unknown" is strictly more faithful than
"allow text↔numeric comparison".

## Change

### 1. Analyzer: `StringConst` → `unknown` (`internal/analyzer/analyzer.go`)

`analyzeExpr` now returns `catalog.Type{Name: "unknown"}` for
`*parser.StringConst`, mirroring the existing `NullConst` / `ParamRef` /
`CastExpr` cases. All analyzer compatibility helpers already short-circuit
`unknown` as a wildcard (`isComparable`, `isAssignable`, `isStringLike`,
`isNumericLike`, `isTimestampLike`, `sameOrCompatible`), so:

- `bigserial_col = '1'` type-checks (both operand orders, all six comparisons);
- `INSERT ... VALUES ('1234')` into numeric columns keeps working
  (`isAssignable` unknown short-circuit);
- `||`, `LIKE`, CASE unification keep working (`isStringLike(unknown) = true`);
- `text_col = 5` still errors (the column is `text`, not `unknown`).

Output typing is unaffected: RowDescription type OIDs are derived by the
planner/executor from `planner.StringConst`, not from analyzer types.

Behavioral note: `LIMIT 'x'` no longer errors at analysis (the literal is
`unknown`, which is integer-like); it errors at runtime instead
(`limitOp.Open`, 42804) — consistent with the codebase's documented policy of
deferring string-literal validation to runtime (M0097-0003). PG accepts
`LIMIT '5'` and rejects `LIMIT 'x'` at parse time; goopg now accepts both at
analysis and rejects the malformed one at execution.

### 2. Runtime sibling: index-probe key coercion (`internal/executor/operators_ddl.go`)

The seq-scan comparison path already coerced cross-kind operands
(`compareDatum` → `promoteCrossKind` → `tryParseStringAs`,
`internal/executor/expr.go`), but its **sibling** — the B-tree probe-key
builder `encodeBTreeKeyForColumn`, shared by index scan / index-only scan /
upsert probes — required the Datum kind to already match the column
(`ERROR: column "ID" is not integer at runtime`).

`encodeBTreeKeyForColumn` now coerces a `KindString` probe datum to the
column's runtime kind first (int → `tryParseStringAs(KindInt)`, numeric,
timestamp/date → `KindTime`), so a probe built from a quoted literal encodes
symmetrically with the backfilled rows. Malformed input raises PG's exact
error: `22P02 invalid input syntax for type bigint: "abc"`. Backfill values
arrive pre-decoded with the correct kind and are never rewritten.

## Tests

- `internal/analyzer/coerce_test.go` — `TestUnknownStringLiteralCoercion`:
  positive matrix (bigserial/int8/int4/numeric/date × six operators × both
  operand orders, plus `||`/`LIKE`/text cases), negative guard
  (`text_col = 5` both orders still 42804). Follows the
  `TestSerialPseudotypeIntegerTypeCheck` precedent.
- `internal/analyzer/analyzer_test.go` — `LIMIT 'x'` assertion replaced with
  `LIMIT true` (concrete non-integer still caught at analysis; the string case
  is now a runtime 42804 per the policy above).

## Verification

- `go test ./internal/analyzer/ ./internal/planner/ ./internal/executor/ ./internal/parser/` green.
- Live: `WHERE ID = '1'` returns the row via index probe; `WHERE ID = 'abc'`
  raises `22P02`; reversed order and inequalities work (verified against the
  WordPress dataset on the wp instance).
- TPC-H Q12/Q13 spot-check + regress parity: see the commit's gate run.
