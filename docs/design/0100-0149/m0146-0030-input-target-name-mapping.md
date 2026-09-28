# M0146-0030 — input-target derivations decline on renamed keys

Status: landed 2026-09-28 (also closes M0146-0031's panic). Code:
`inputTargetNamesPresent` and the three derivations `deriveSortInputKeep`,
`deriveAggregateInputKeep`, `deriveWindowInputKeep`
(internal/optimizer/{sort,group,window}_input_target.go). Test:
`internal/optimizer/input_target_alias_test.go`.

## Defect

The B-01c input-target stamps are compute-only: they derive which input
columns a Sort, Aggregate or WindowAgg reads, stamp that set on the node, and
assert that every key read survives. The derivation maps key reads to input
positions **by column name**. A column-alias list, as in
`SELECT * FROM (SELECT f1 FROM int4_tbl) ss(z) ORDER BY z`, renames the
binding but not the leaf's output schema. The sort key is named `z` while the
input column is named `f1`, so the keep omitted the key and the assert
panicked on valid SQL. That hit regress `join.sql:3217` and `arrays.sql:682`,
and aborted the regress runner.

## Rule

A key name that matches no input column means the name-based mapping failed.
The derivation now reports unknown (decline) instead of a keep that silently
omits the key. Unknown is the stamps' documented safe answer, since the
stamp changes nothing today. All three sibling sites change together. The
assert is kept. Its tests now hand-build a known-but-uncovering stamp,
because the derivation no longer produces one.

## Residual

A positional mapping (key `ColumnRef.Index` → input position) would keep the
stamp known across renames. That is ledgered for when a stamp starts being
applied.

## Exposed

With the crashes gone, regress `join` and `arrays` run to completion and
show two pre-existing wrong-results defects: a LATERAL outer reference bound
to a same-named column of the wrong relation (M0146-0032), and `array_agg`
over arrays not building a multidimensional array (M0146-0033).
