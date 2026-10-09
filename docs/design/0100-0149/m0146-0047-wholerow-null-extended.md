# M0146-0047 — the whole-row value of a null-extended row is NULL

Status: done (2026-10-03). Parent: M0146 (banner item 2a, third S2 batch).

## The defect

`SELECT count(b) FROM wa a LEFT JOIN wb b ON …` counted every outer row on
goopg: 2000 where PG 18.3 counts 1715. In PG, a whole-row Var of the
nullable side is a column of the join's output tuple, and the join's null
extension makes that column NULL. A real row whose fields are all NULL is
still a non-NULL composite `(,,)`.

goopg resolves a whole-row reference (`b`) to a `RowExpr` over b's
columns, so a null-extended row produced the non-NULL composite `(,,)`.
`b IS NULL` was already right, because a row of all-NULL fields IS NULL
under SQL's row semantics. The value itself was wrong: `count(b)`,
`coalesce(b::text, …)` and `b::text` all differed.

## The fix

Whether a row is null-extended cannot be read from its fields: an
all-NULL real row and a null-extended row look the same. What can be read
is a column that is never NULL in a real row. `RowExpr.NotNullElem`
records, for a whole-row reference to a relation, the element reading the
relation's first NOT NULL column (a primary key, for instance), stored as
1 + index.

`evalRowExpr` evaluates that element first and returns NULL when it is
NULL. Only an outer join's null extension can produce that.

The witness is an element position rather than a separate expression, so
the two hand-written rewriters that rebuild a `RowExpr`
(`remapColumnRefsToSchema`, `shiftColumnRefsBy`) only copy the field, and
the reflection-based cloners carry it automatically.

## Verification

- Scratch probes against PG 18.3: `count(b)` is 2 on both (was 4 on
  goopg). `b IS NULL`, `b IS NOT NULL` and `coalesce(b::text, 'NUL')` on
  the unmatched rows match PG. A matched row with NULL non-key fields
  stays a non-NULL composite. `count(b)` over the table alone is
  unchanged.
- `TestWholeRowOfNullExtendedRowIsNull` pins those cases.
- Regress A/B: `rowtypes`, `join`, `subselect`, `with` and `aggregates`
  are byte-identical.
- Gates: units, spotcheck, sweep 96/96, TPC-H arm.

## Residuals

- A relation with **no** NOT NULL column, or a derived table, has no
  witness. Its null-extended rows still yield `(,,)`. PG keeps the
  whole-row Var as a join output column; goopg would need the scan to
  publish a whole-row datum or a non-NULL marker (ledgered).
- `b.*` in expression context (`expandQualifiedStarToRowExpr`) carries no
  witness (ledgered).
- **Found, filed as M0146-0047a (S2):** a whole-row reference across a
  join loses every column the query does not otherwise read. `SELECT b
  FROM wa a LEFT JOIN wb b …` returns `(1,,)` where PG returns `(1,10,a)`.
  Pre-existing on HEAD; column narrowing does not count a whole-row
  reference as reading all of the relation's columns.
- `row_to_json` is not implemented, and a cast target list names the
  column `text` where PG names it `b`. Both are separate gaps.
