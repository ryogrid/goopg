# M0149\-0001h1a72 — range operators

## Problem

goopg carries a range value as its canonical text \(M0134\-0173\), and no operator understood it:
`int4range(1,5) @> 2` was routed to the box operator, and `&<`, `&>`, `-|-`, range `+`, `*`, `-` and ordering failed.

## What PG does

pg\_operator.dat maps each operator over anyrange to a rangetypes.c function: `range_contains_elem`, `range_contains`,
`elem_contained_by_range`, `range_contained_by`, `range_overlaps`, `range_before`, `range_after`, `range_overleft`,
`range_overright`, `range_adjacent`, `range_union`, `range_intersect`, `range_minus` and the btree comparisons over
`range_cmp`. They all work on deserialized bounds through `range_cmp_bounds`, which orders an exclusive lower bound after
its value and an exclusive upper bound before it.

## Design

- The parser gains the operator codes `&<`, `&>` and `-|-`.
- `rangeOpCall` \(internal/optimizer/range\_ops.go\) resolves an operator with a built\-in range operand by static type into a
  FuncCall named after the pg\_proc function, with the range type after a `|`, because constant folding can turn a range
  operand into an untyped string. An untyped literal beside a range takes the range's type.
- `internal/executor/range_ops.go` deserializes the text with `rangeParse` and the subtype's input function and ports the
  function bodies, including `bounds_adjacent`'s empty\-range test for the discrete types.
- `=` and `<>` stay ordinary operators: the canonical text already compares equal for the discrete types, and equality
  joins key on them.

## Not covered \(M0149\-0001h1a86\)

- `numrange(1.50, 2) = numrange(1.5, 2)` is false: the text keeps the numeric scale.
- ORDER BY and btree order over a range column compare the text.
- isempty, lower\_inc, upper\_inf, range\_merge and the other range functions; user\-defined range types and multiranges.
- EXPLAIN prints a literal range operand without its `::int4range` label.
