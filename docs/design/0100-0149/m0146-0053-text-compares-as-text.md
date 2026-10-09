# M0146-0053 — character strings compare as text

Status: done 2026-10-06 (`1c612c421`).

## Symptom

- `SELECT '(999,9)'::text > '(1241,10)'::text` returned `f`, also with
  `COLLATE "C"`. PG 18.3 returns `t`.
- The same class (probe of 30 statements against PG, then table data):
  - `'(1,2)'::text = '(01,2)'::text` was `t` (PG `f`);
  - `'{10}'::text < '{9}'::text` was `f` (PG `t`);
  - `max` / `min` / `ORDER BY` / `string_agg(... ORDER BY x)` / a WHERE
    filter over such values were wrong;
  - a case-different UUID-shaped `char` / `varchar` pair compared equal
    (PG: not equal).

## Cause

- Records, arrays and tids travel through the executor as text Datums
  (`KindString`).
- `compareDatum` cannot see a value's type, so its `KindString` arm
  guesses from the value's shape:
  - `(…)` on both sides is compared element-wise as a row
    (`compareRowStrings`, M0097-0115);
  - `{…}` as an array (`compareArrayStrings`, M0097-0117);
  - `X/Y` as a pg_lsn;
  - a uuid shape is case-normalised.
- A real text value took the same arms.
- PG instead picks the comparison function from the operand type at plan
  time: text uses `bttextcmp` / `varstr_cmp`
  (`src/backend/utils/adt/varlena.c`), which is byte order under the C
  collation.

## Change

### Comparison helpers (`internal/executor/text_compare.go`)

- `isCharacterStringType` covers text, varchar, bpchar, name and "char".
  `exprIsCharacterString` resolves an expression's static type through
  `optimizer.ExprResultType`.
- `textShapeAmbiguous(a, b)` is true only when both values are owned
  strings whose shape would make `compareDatum` guess.
- `compareDatumTyped(a, b, pos, expr)` / `compareDatumPlain(a, b, pos,
  plain)` compare plainly when the expression is a character string and
  the shape is ambiguous. Otherwise they call `compareDatum`.
- The type is consulted only on the ambiguous path, so ordinary
  comparisons pay nothing. An expression whose type does not resolve keeps
  the guess.

### Sites

- **BinaryOp comparisons**, through all three evaluators:
  - the interpreted twin (`expr.go`);
  - the compiled twin (`exprnode.go`, `payload[16]` bit 2 set at compile
    time);
  - the batched filter (`expr_batch.go`).

  The check runs ahead of the pg_lsn shape test, after the bpchar operand
  rule (`binaryTextComparison`).
- **Other operators:** IS DISTINCT FROM, row-to-row comparison and
  GREATEST/LEAST.
- **The ordering family**, which must agree with its sorted inputs or
  merges and group boundaries break:
  - `sortOp.lessKeyVals` / `lessRows`, the incremental sort,
    `sortPrefixEqual`, `mergeKeysLess` (Gather Merge, Merge Append);
  - window peers and order keys, and the distinct operator's output order;
  - merge join keys (`joinOp.mergeKeyText`, a pair is text when both
    sides are);
  - aggregate ORDER BY, WITHIN GROUP and hypothetical-set keys;
    grouping-set output order; the sorted-transport order check.
- **min/max:** the transition and the parallel combine
  (`combineAggRuntime` takes the aggregate's argument).

### Planner

- `exprType` types `row(...)`, a `FuncCall` named row, as `record`. It
  was unknown, so a VALUES column of rows fell back to `text`. With the
  new rule such a column would have compared as text and lost its
  element-wise order.
- `RowExpr` is typed `record` as well.

## Verification

- `TestTextComparesAsText` (`internal/executor/text_compare_test.go`):
  - 33 statements, each through both builders: the legacy builder uses the
    interpreted BinaryOp, the slab builder the compiled one;
  - every want is PG 18.3's output;
  - covers text and varchar, records, arrays, tids, pg_lsn- and
    uuid-shaped text, VALUES and table data (scan-absorbed predicate,
    arena strings), a join, DISTINCT, aggregates and GREATEST/LEAST;
  - 42 of 66 runs fail at HEAD.
- `TestHashJoinBpcharUUIDCaseMissParity` pinned the old scalar `=` =
  `t`. It now pins PG's `f`, which agrees with the (unchanged) empty
  join.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24 MATCH;
  - sf025 96/96 with no verdict change. Its Q54/Q87/Q8 runtime moves
    were re-timed solo against a HEAD binary and came out equal;
  - fire set PASS, no plan changed;
  - ea-ratchet PASS.
- Regress A/B over 21 files against a HEAD binary (`--port`, fresh
  clusters; text, varchar, char, strings, rowtypes, arrays, uuid,
  pg_lsn, select, select_distinct, union, aggregates, join, subselect,
  with, window, tid, create_index, plpgsql):
  - 17 byte-identical;
  - uuid moved only in time-dependent uuidv7 rows;
  - join, subselect and plpgsql moved within their known HEAD flaps.

## Not covered

- `||` has the same class of defect: `'a' || '{9}'::text` returns
  `{a,9}` (array concatenation); PG returns `a{9}`. Filed as M0146-0074
  (S2).
- Comparison sites still on the guess: ANALYZE histogram/correlation
  sorting, extended-statistics ndistinct, PL/pgSQL's internal comparison
  helpers, range-type bounds, window RANGE offsets, and a user-defined
  DISTINCT aggregate's argument sort.
- A value whose static type does not resolve (e.g. a domain over text)
  keeps the guess.
- `pg_typeof(x)` for a VALUES column of `row(...)` still prints `text`
  (PG `record`). The comparison side reads the corrected `record` type;
  `pg_typeof`'s own type route differs.
- The structural fix is a typed Datum: a tag for record, array and tid
  text, or real kinds. Then `compareDatum` would no longer need to guess
  at all.
