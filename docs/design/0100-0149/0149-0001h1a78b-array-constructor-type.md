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
