# M0146-0005 slice 19 (M0146-0005r): INTERSECT smaller-input swap

PG's generate_nonunion_paths (prepunion.c) puts the INTERSECT input with
fewer groups on the left (`dLeftGroups > dRightGroups` swaps; EXCEPT is
never swapped). goopg kept the written order.

## Change

`swapIntersectInputs` (windowsetoppaths.go) runs at the top of
`createSetOpPaths`. It compares the arms' `setOpArmGroups` counts and
swaps the inputs when the left one has more groups. The swapped node pins
its output schema to the written first arm (`SetOp.pinnedSchema`), so
column names and types stay those of the query as written, as PG's
set-operation target list does.

## History

The first attempt (before slice 18) regressed Q14 at SF0.25 from depth 7
to 2: goopg swapped cross_items' arms where PG did not, because goopg's
web_sales arm was underestimated (the missing eq_selec). With slice 18 in
place Q14 keeps PG's order and is unchanged.

## Results

- Q38 (SF0.25 and SF1): depth 3 -> 4. Both INTERSECT levels now take
  PG's child order (`q38-plans.txt`); the remaining record is PG's
  partial Unique + Gather Merge.
- Fire set: only Q38 fires; PASS, no timeouts. Sweep 96/96. TPC-H census
  identical; spotcheck PASS; acceptance arm 24 MATCH.
