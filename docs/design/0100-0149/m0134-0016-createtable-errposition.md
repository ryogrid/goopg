# M0134-0016 — PG-faithful `errposition` for CREATE TABLE validation errors

Status: **LANDED** (2026-08-20) — bucket A of `create_table.sql` only; the case
remains `failed`. See `.ralph/deferral_ledger.md` (2026-08-20, M0134-0016) for
the buckets left unshipped.

## Problem

`postgres/src/test/regress/expected/create_table.out` annotates most CREATE TABLE
validation errors with an `errposition` — the wire field `P`, which psql renders
as a `LINE n: <statement excerpt>` / `^` caret pair. goopg emitted none of them.
At HEAD this accounted for **114 of the 178 missing diff lines (~64%)** of the
case, with *zero* message-text mismatches underneath: the messages were already
byte-correct, only the position was absent.

## Root cause — a sentinel collision, not a missing feature

`ExecError.Pos` is **0-based**, and `0` doubles as the "unset" sentinel. The wire
layer emits the field only when the position is positive, converting to the
protocol's 1-based numbering:

```go
// internal/postmaster/copy.go:854-858
// ExecError.Pos is 0-based (0 = unset); wire protocol FieldPosition is 1-based.
if ee.Pos > 0 {
    fields = append(fields, libpq.ErrorField{Code: libpq.FieldPosition, Value: strconv.Itoa(ee.Pos + 1)})
}
```

Both partition validators computed a single position up-front from the
*statement* and stamped it on every error they raised:

- `validatePartitionKey` — `internal/executor/operators_ddl_partition.go:144`
- `validatePartitionChildBounds` — same file, `:346`

In a regress file each statement is submitted on its own, so `CREATE` sits at
byte offset 0 and `s.Pos()` is `0` — exactly the sentinel. The annotation was
therefore dropped silently. Even had it survived, it would have pointed at the
`CREATE` keyword rather than at the offending token.

This is a twin-shaped bug: the position was *computed once at the wrong
granularity*. PG never does this — it threads a per-node `location` from the
grammar down into `parser_errposition`
(`postgres/src/backend/parser/parse_expr.c:585-601`), so the caret always lands on
the node that was actually rejected.

## Fix

Stamp each error with the position of the **offending sub-node**, which every
`parser.Expr` already carries via `.Pos()` (`internal/parser/expr.go:379-513`):

- `validateDefaultExpr` — the `ColumnRef`, aggregate `FuncCall`, SRF `FuncCall`
  and `SubqueryExpr`/`ExistsExpr`/`ArraySubqueryExpr` arms use `v.Pos()`. The
  caller-supplied `pos` survives only as the value forwarded through pure
  structural recursion (`BinaryOp`, `CastExpr`, …), which never raises.
- `validatePartBoundExpr` — same treatment. The aggregate arm additionally
  replaced its `containsColumnRef(arg)` boolean probe with a real recursive
  `validatePartBoundExpr(arg, …)` call: this preserves PG's priority (a column
  reference inside the aggregate's arguments outranks the aggregate error) *and*
  yields that inner reference's own position, so the caret lands on `a` in
  `sum(a)` rather than on `sum`.
- `validatePartitionKey` — the strategy and key-column errors operate on plain
  `string` fields, so the positions had to be carried from the parser (below).

### Parser: two positions the AST was discarding

Unlike the expression validators, `PARTITION BY` errors had no node to point at:
`parsePartitionKeyCols` unwraps each key's `ColumnRef` into a bare `colName
string` and throws the node away, and the strategy is a bare `Method string`.
`PartitionByClause` therefore gained two fields carrying positions the parser had
already computed:

- `MethodPos int` — byte offset of the strategy token (`MAGIC` in
  `PARTITION BY MAGIC (a)`), captured at both parse sites before the token is
  consumed.
- `KeyColPos []int` — byte offset of each plain-column key's `ColumnRef`
  (including through a `CollateExpr` wrapper); `0`, i.e. no caret, for expression
  keys, matching the `colName == ""` branch in the validator.

This is the PG-faithful shape rather than added plumbing: upstream carries the
identical pair as `PartitionSpec.location` and `PartitionElem.location`
(`postgres/src/include/nodes/parsenodes.h`).

## Faithfulness rule applied

Positions were added **only** where PG's expected output shows a `LINE`/`^` pair,
and never blanket-applied. Errors PG reports without a position were left alone —
verified case by case against `create_table.out`: the "list strategy with more
than one column", operator-class and no-default-opclass errors keep `Pos: 0`
deliberately.

The line number is never computed in goopg. The protocol field is a byte offset
into the statement text and psql derives `LINE n` from it, which is why the
multi-line `) PARTITION BY MAGIC (a);` case renders as `LINE 3:` with no
line-counting logic anywhere in the engine.

## Verification

- New `TestDefaultExprErrposition` and `TestPartitionKeyColumnErrposition`
  (`internal/executor/default_validate_test.go`) assert `ExecError.Pos > 0` **and**
  that it equals the byte offset of the offending token — a bare non-zero
  assertion would not have caught a caret pointing at the wrong token.
- Sibling check: `validateDefaultExpr` is reached from both the `CREATE TABLE`
  path (`internal/executor/operators_ddl.go:3319`) and the `ALTER TABLE ... ALTER
  COLUMN SET DEFAULT` path (`:10344`); both were verified to propagate the
  corrected position unchanged. No third caller exists.
- `scripts/pg-regress-runner.sh --verbose create_table`: **762 → 610 diff lines**,
  `-LINE` **57 → 29**, `+ERROR` unchanged at **17** (no new divergence).

## Not fixed here

Errors outside these three validators still lack positions — notably the
temp-schema pair (`only temporary relations may be created in temporary
schemas`, `cannot create temporary relation in non-temporary schema`) and the
clause-level `validatePartitionChildBounds` family (`invalid bound specification
for a %s partition`, partition-overlap, default-partition conflict, the
NULL/FROM-TO arity errors). The latter have no single offending expression node;
they would need parser positions for the `FOR VALUES` / `WITH (MODULUS …)` /
`DEFAULT` clause tokens. Both are ledgered.
