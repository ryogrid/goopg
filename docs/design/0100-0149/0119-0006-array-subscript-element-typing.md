# `arr[i]` yields the element type's Datum, not text (M0119-0006)

Status: accepted — landed 2026-08-11
Milestone: M0119-0006 (deferral-ledger backlog consumption)
Source ledger row: `2026-08-11 | M0119-0006 | … | Element COMPARISON is still
lexicographic …` (opened by the array-element image slice,
[`0119-0006-array-element-pg-images.md`](0119-0006-array-element-pg-images.md))

## The defect

The previous slice gave `interval[]` / `uuid[]` / `numeric[]` PG's own element
images on disk, and the ledger row it left behind said the symptom had NOT gone
away:

```sql
SELECT c[1] = c[2] FROM t;   -- ARRAY['1 mon','30 days']::interval[]
-- PG 18.3: t          goopg: f
```

Storage was no longer the cause. The array-subscript evaluator
(`evalFuncCall`'s `case "array_subscript"`, `internal/executor/expr.go`) decodes
the array to its TEXT rendering, slices out the element, and — apart from a
plain-integer fast path — returns `NewStringDatum(elem)`. A `KindString` never
reaches `compareDatum`'s `interval_cmp_value` or numeric ladders, so `=` and
`<`/`>` compared the two element SPELLINGS. Upstream has no such step: PG's
`ExecEvalSubscriptingRef` returns a Datum *of the element type*, because the
array header carries `elemtype` and the executor deforms the element with that
type's own layout.

The same one wrong-shape Datum produced four distinct wrong answers, all
captured from the PG 18.3 reference cluster on port 65432:

| query | PG 18.3 | goopg (before) | why goopg was wrong |
|---|---|---|---|
| `c[1] = c[2]` over `{'1 mon','30 days'}::interval[]` | `t` | `f` | `"1 mon"` ≠ `"30 days"` as text; equal to `interval_cmp_value` |
| `n[1] = n[2]` over `{'1.50','1.5'}::numeric[]` | `t` | `f` | numeric equality is value-based, text equality is not |
| `n[3] > n[4]` over `{…,'10','9'}::numeric[]` | `t` | `f` | `'1' < '9'` bytewise |
| `f[1] > f[2]` over `{9.5,10.2}::float8[]` | `f` | `t` | `"9.5" > "10.2"` bytewise |

`a[1] + a[3]` over an `int4[]` column did not even reach the executor: it failed
analysis with `42804 operator + requires numeric operands`.

## Why it took three sites, not one

The element type has to be known at three independent places, and **all three
had the same blind spot**: a user array column is
`catalog.Type{Name: "<ELEMENT>", IsArray: true}` — the element name with a flag,
never `"elem[]"` and never the catalog's `"_elem"` spelling (memory:
`goopg_array_column_isarray_codec`). Every one of the three probed only for the
spellings it did not get, and fell through to `text`.

1. **`internal/analyzer/analyzer.go`, `case *parser.ArraySubscriptExpr`** — typed
   every user-array subscript as `text`, which is what rejected `a[1] + a[3]`
   before execution. Now returns the element type for `IsArray` (and for the
   `_elem` spelling, which it also never handled).
2. **`internal/planner/planner.go`, `exprType`'s `array_subscript` arm** — same
   probe, same fallthrough. This one decides `pg_typeof` and the wire type OID.
3. **`internal/executor/expr.go`, the `array_subscript` evaluator** — had no way
   to ask: `exprType` is planner-internal. `resolveExpr` now stamps the element
   type it already computes onto `FuncCall.ReturnType`, which `exprType` treats
   as its own override arm (so the stamp changes no inference), and the executor
   re-types the element text through `arraySubscriptElemDatum`.

The analyzer and the planner are the sibling pair here — they answer the same
question for different consumers and had to move together
(`pattern_sibling_paths_must_agree`).

## The fourth site: five clone helpers were eating the stamp

With all three edits in place the executor still saw an EMPTY `ReturnType`,
while `pg_typeof(n[1])` correctly reported 1700. The stamp was applied and then
discarded: five `case *FuncCall:` clone-with-rewritten-children helpers rebuild
the node field by field and simply did not list `ReturnType`.

- `internal/planner/foldconst.go` `FoldConstants` (the one on this query's path)
- `internal/planner/planner.go` `remapColumnRefsToSchema`, `shiftColumnRefsBy`
- `internal/planner/unnest.go` `shiftExprColumnIdx`, and the local `rewriteIdx`

This is not specific to subscripts. `ReturnType` is also how a **user-defined
function's** return type reaches the executor and the wire (`resolveExpr` sets
it from the routine's catalog row), so any UDF call that survived constant
folding or a column remap was silently re-typed as unknown downstream. Only one
of the six construction sites in the package carried the field; the fix carries
it in all six.

## Scope: which element types are re-typed

`arraySubscriptElemDatum` routes only the types whose Datum kind changes the
ANSWER, and returns `false` — leaving the pre-existing text behaviour — for
everything else, including when the element text does not parse:

- `interval` → `KindInterval` via `parser.ParseIntervalBody`, the same tokenizer
  `interval_in` and the storage-encode arm use, so `c[1]` still re-renders
  through `formatInterval` exactly as the array codec spelled it.
- `numeric` / `decimal` → `KindNumeric`, display scale preserved (`n[1]` still
  prints `1.50`, not `1.5`).
- `float8` / `float4` / `double precision` / `real` → also `KindNumeric`,
  because goopg has no `KindFloat`: a float8 COLUMN already decodes to
  `KindNumeric`, so this makes the subscript agree with its own scalar column
  rather than inventing a shape. A spelling `parseNumeric` cannot take
  (scientific notation) falls through to text rather than guessing.

`date` / `time` / `timestamp` are deliberately NOT routed: their ISO spellings
sort exactly the way their values do, so the text path is already correct, while
`KindTime` rendering is not yet byte-identical to the array codec's element
spelling — routing them would trade a comparison fix for a rendering
regression. That is a ledger row, not a guess. `uuid`, `bool` and the text-likes
are correct as text for the same sorting reason, and `int*` is already covered
by the evaluator's own integer fast path.

## Gates

- New `internal/executor/array_subscript_elem_type_test.go`: 21 end-to-end SQL
  assertions (both planner and executor halves; a `compareDatum` unit test would
  have passed throughout, which is exactly how this survived the storage slice),
  every `want` captured from the PG 18.3 reference cluster. A second test pins
  the shapes that already worked — `int4[]` elements, and `ARRAY[…]`
  constructor subscripts that have no column type behind them — because the
  planner edit changed what `exprType` reports for EVERY array subscript.
- `internal/analyzer`, `internal/planner`, `internal/executor` package suites,
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`,
  `scripts/tpch-spotcheck.sh` (Q12=2 / Q13=35),
  `go test -run TestPort_RegressSuite ./internal/testport/`.

## Left open (ledger)

- `date` / `time` / `timestamp` / `timestamptz` / `timetz` elements are not
  re-typed; upstream types every subscript through the element type's input
  function.
- `ReturnType` carries only the type NAME, not `Args`, so an element typmod
  (`numeric(10,2)[]`) is dropped at the stamp.
- Array SLICES (`a[1:2]`) are not a typing gap at all — the lexer rejects the
  `:` (`lex error at byte 10: unexpected character ':'`), so PG's slice syntax
  is unimplemented at the parser, one layer below this slice.
