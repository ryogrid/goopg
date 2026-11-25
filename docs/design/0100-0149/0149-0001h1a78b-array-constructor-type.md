# Array constructor type and subscript element datums (M0149-0001h1a78b)

## Problem

`select (array[1,2])[1] + 1` failed with 42804 `operator + requires numeric operands`; PG 18.3 returns 2.
`('{2020-01-31}'::date[])[1] + 1` failed with 42883 `operator + requires integer operands`; PG returns 2020\-02\-01.

## Design

- **Analyzer.** `arrayConstructorType` \(internal/parser/analyzer/analyzer.go\) types `ARRAY[...]` the way
  transformArrayExpr \(parse\_expr.c\) does: the array of the elements' common type \(select\_common\_type\).
  - Unknown literals do not vote.
  - Numeric elements promote along PromoteNumericType's lattice.
  - An array element makes a multidimensional array of the same type.
  - Anything else keeps the earlier `text[]`, which is the conservative reading.
- **Chained subscripts.** goopg nests `a[2][1]` as two ArraySubscriptExprs, where PG has one SubscriptingRef with
  two subscripts. The outer subscript of an element fetch over an array keeps the element type instead of
  subscripting a scalar.
- **Executor.** `arraySubscriptElemDatumCtx` \(internal/executor/expr.go\) reads a date element with the
  session's date input settings. array\_out spelled the element in the session DateStyle, so this reads it back
  the same way. The result is a date datum, and `+ 1` dispatches to date\_pli.
- **Column names.** `systemTypeColname` \(internal/parser/figurecolname.go\) drops array bounds. PG's
  FigureColnameInternal takes the type name's last component, so `x::date[]` is named `date`.

## Not done

- The planner's exprType for `array_construct` still takes the first element's type, not the common type.
  The values agree in every probe, because the executor re\-types per element.
- Multidimensional subscripts return fragments of the array text. This is M0149\-0001h1a78e \(S2\).
- Other datetime element types \(timestamp, time\) still come back as text from a subscript.

## string\_to\_array \(M0149\-0001h1a78c\)

- `string_to_array(string, delimiter [, null_string])` is a port of varlena.c's split\_text and
  split\_text\_accum\_result \(`splitTextToArray` in internal/executor/expr.go\).
  - The function is not strict: a NULL string gives a NULL result, and a NULL null\_string matches nothing.
  - A NULL delimiter makes one element per character.
  - An empty delimiter makes the whole input one element, and an empty input gives `{}`.
- The planner gives string\_to\_array and regexp\_split\_to\_array the result type text\[\]; before this change
  pg\_typeof said `unknown`.
- array\_out quotes an element that spells NULL in any case, so it reads back as text and not as a null. Both text
  array formatters \(formatTextArray, formatTextArrayWithNulls\) now do this.
- Not done: the separator search is byte\-wise. PG's text\_position honours nondeterministic collations.

## string\_to\_table \(M0149\-0001h1a78f\)

- text\_to\_table's rows are exactly `unnest(string_to_array(...))`: the same split\_text, with no rows for a NULL
  or empty string. goopg plans the function as that unnest rather than adding a plan node.
  - **FROM:** `planFromStringToTable` builds a FromUnnest over `string_to_array(...)`. `FromUnnest.FuncName` and
    `FuncArgs` keep the function's name and arguments, so EXPLAIN prints `Function Scan on string_to_table` and
    the VERBOSE `Function Call` line shows its own arguments.
  - **SELECT list:** the bare target and the nested form are UnnestCols with `FuncName: "string_to_table"`. The
    VERBOSE ProjectSet Output deparses them under that name.
- **Estimates:** string\_to\_table has no support function, so the estimate is its prorows of 1000, both in the
  Function Scan \(EstimateRows\) and in the ProjectSet's largest\-SRF row count.
- **Not done:** `ROWS FROM (string_to_table(...))` does not go through this path.
