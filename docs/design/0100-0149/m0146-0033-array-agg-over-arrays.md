# M0146-0033: array_agg over an array input stacks the arrays one dimension deeper

Status: landed 2026-10-02 (banner item 2a, S2).

## Defect

    select array_agg(x) from (values (array[1]),(array[2])) v(x);

goopg returned `{"{1}","{2}"}`, a one-dimensional text array whose
elements were the inputs' text. PG returns `{{1},{2}}`
(`array_agg_array_transfn` / `array_agg_array_finalfn`,
src/backend/utils/adt/array_userfuncs.c; regress arrays.out). None of
PG's input checks fired either: a NULL input, an empty input and a
mismatched shape were all accepted silently.

goopg's `array_agg` had one arm, the scalar `array_agg_transfn`, which
quotes each element as text. Two input type spellings also reached it:

- a table column's type is `{Name: "int4", IsArray: true}`;
- a VALUES or expression array is `{Name: "int8[]"}` (IsArray false).

## Change

`internal/executor/operators_join_agg.go`, `applyAgg` `array_agg`: an
array input (either spelling) takes the anyarray arm. Each input's array
text is kept as a whole sub-array. `finishBuiltinAgg` sorts by the ORDER
BY keys when given, then joins the inputs into `{sub1,sub2,...}`, one
dimension deeper.

The checks follow `accumArrayResultArr`'s order:

| input | error |
|---|---|
| NULL | `22004 cannot accumulate null arrays` |
| first input empty | `2202E cannot accumulate empty arrays` |
| later input with different dims (an empty one included) | `2202E cannot accumulate arrays of different dimensionality` |

`arrayTextShape` reads the dimension lengths from the array text by
following the first element down each level; quoted elements are skipped.

`internal/optimizer/planner.go`: both `array_agg` result-type sites (the
aggregate's `outType` and `exprType`) return the input's own array type
for an array input, as anyarray → anyarray does. Before, they appended
another `[]` (`int8[][]`), which no type lookup resolves, so
`pg_typeof(array_agg(x))` printed `text`.

The window path (`array_agg(x) over (...)`) shares the accumulator and
matched PG in a probe. array_agg is never split across parallel workers
or spilled (`AggregateIsOrderSensitive`, `agg_state_serial.go`), so
there is no combine or serialise sibling.

## Verification

`TestArrayAggOverArraysMatchesPG` (executor) checks 11 queries against PG
18.3 output: 7 results (ORDER BY, quoted text elements, NULL elements,
3-D, FILTER, plus the scalar arm unchanged) and the 4 errors.
`TestArrayTextShape` pins the dimension reader.

- Regress A/B: `arrays` shrinks 3279 → 3205 diff lines. Seven array_agg
  result rows and all three error lines now match PG; no line is new.
  `aggregates` and `window` are byte-identical.
- Gates pass: units, spotcheck (Q12=2, Q13=33), sweep, arm (24 MATCH),
  fire set (no TPC-DS plan changed) and ea-ratchet (10/10).

## Left open (ledgered)

- An input with explicit lower bounds (`'[2:3]={1,2}'::int[]`) raises
  0A000. goopg's array datum is text, and the dimension reader does not
  decode the bounds; PG keeps them on the result.
- `pg_typeof(array_agg(array[1]))` prints `bigint[]` where PG prints
  `integer[]`. This is the existing integer-literal typing gap (literals
  type bigint), not array_agg.
- Found while testing: an `ARRAY[...]` constructor over text does not
  quote its elements on output. `array['a,b','c']` prints `{a,b,c}`
  (PG `{"a,b",c}`). Filed as the S2 M0146-0045, not worked.
