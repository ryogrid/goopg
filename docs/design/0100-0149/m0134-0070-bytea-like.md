# M0134-0070: `bytea` operand support for the SQL `LIKE` operator

Status: accepted. Scope: `strings.sql` regress case — the `bytea LIKE` bucket
(`SELECT * FROM byteatest WHERE a LIKE '%1%';` where `a` is a `bytea` column,
strings.sql:535-536; PG expected `(0 rows)`, goopg raises
`ERROR: operator LIKE requires string operands`).

## Problem

PG resolves `a LIKE '%1%'` with a `bytea` column through the `bytealike`
operator (`~~(bytea,bytea)`, `pg_operator.dat:2383-2384`), whose byte-wise
semantics (`like.c:326`) match SQL `LIKE` on raw bytes with no charset/encoding
awareness. goopg dispatches `LIKE`/`NOT LIKE` syntactically rather than through
operator resolution, and the gate lives in the **analyzer**, not the executor:
`internal/parser/analyzer/analyzer.go:1499-1505` (`OpLike`/`OpNotLike`) rejects
any operand pair unless `isStringLike(left) && isStringLike(right)`, with
SQLSTATE 42804 and no `(got …)` suffix — exactly the error the diff shows. The
executor's own LIKE arm (`expr.go:1823-1838`) appends a `(got left.Kind=…)`
suffix, so it is not the source; the bytea column never reaches it.

`isStringLike` (`analyzer.go:3162`) → `isStringTypeName` (`analyzer.go:3154-3160`)
lists only text/varchar/char/bpchar/name — `bytea` is excluded. Scalar
`'abc'::bytea LIKE '_b_'::bytea` cases already pass only because the analyzer's
`CastExpr` case (`analyzer.go:1305-1316`) types `expr::type` as `unknown`, and
`isStringLike(unknown)` is true. A typed bytea **column** carries
`catalog.Type{Name:"bytea"}` and is rejected.

## Design

Widen **only** the analyzer's `OpLike`/`OpNotLike` branch to admit a bytea lane.
The rule matches PG's operator resolution: accept when the operands are a
string pair (existing text/name/bpchar/unknown lane) **or** each operand is
either `bytea` or `unknown`:

```go
case OpLike, OpNotLike:
    leftStr, rightStr := isStringLike(leftTyp), isStringLike(rightTyp)
    leftByt, rightByt := isByteaOrUnknown(leftTyp), isByteaOrUnknown(rightTyp)
    if !((leftStr && rightStr) || (leftByt && rightByt)) {
        // existing 42804 "operator LIKE requires string operands" error
    }
```

This admits `bytea/bytea`, `bytea/unknown`, `unknown/bytea`, and all existing
string pairs; it still rejects `bytea LIKE text` (PG: no such operator) and
every non-string/non-bytea pair. ~4 lines, strictly additive — no
currently-passing test can regress.

The executor needs **no change**: `datumAsString` (`expr.go:2106-2114`) already
converts `KindBytes` via `string(d.BytesValue())`, and `matchSQLLike`
(`expr.go:2327`) is byte-wise — precisely PG `bytealike` semantics. The
`bytea ~~ bytea` operator row is already seeded (all 799 `pg_operator.dat` rows
bootstrap at `internal/initdb/pg_operator_bootstrap.go:64`; it is catalog-complete
but unused for routing, which is expected under syntactic dispatch). The
optimizer's `exprType` returns bool for `OpLike` with no operand gate
(`planner.go:12460`), so no planner change.

**Deliberate non-widening:** do **not** widen `isStringLike` globally. It also
feeds the `OpConcat` analysis (`analyzer.go:1491`), where a bytea lane would
wrongly permit `int || bytea` (PG errors). The new bytea lane is local to the
LIKE branch only.

## Out of scope / deferred

- **`ILIKE` on bytea** — untouched; falls to the analyzer default and errors, and
  bytea ILIKE is correctly an error in PG (no bytea case-insensitive operator).
- **`bytea_col LIKE pat ESCAPE e`** — the `LikeEscapePattern` case
  (`analyzer.go:1257-1268`) types the ESCAPE right-operand as `text`, so a
  widened lane would still reject a bytea column with an explicit ESCAPE. Not
  exercised by this bucket's SQL (the passing scalar ESCAPE tests at
  strings.sql:475-476 use `::bytea` casts → `unknown`). A complete fix would
  carry the bytea lane into `LikeEscapePattern` too; recorded as a follow-up,
  not a blocker.

## PG oracle citations

`postgres/src/backend/utils/adt/like.c:326` (`bytealike`, byte-wise match);
`postgres/src/include/catalog/pg_operator.dat:2383-2384` (`~~(bytea,bytea)`);
operator-resolution semantics per `postgres/src/backend/parser/parse_oper.c`
(`bytea` has no cross-type string operator, so the only legal bytea pair is
bytea/bytea and its `unknown`-literal promotions).
