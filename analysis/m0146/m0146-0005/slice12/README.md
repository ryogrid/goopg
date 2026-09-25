# M0146-0005 slice 12 (M0146-0005l): nested loops keep the outer's ordering

Witness: TPC-DS Q44 (`ORDER BY rnk LIMIT 100`). PG merge-joins the two
ranked subqueries on `rnk`, then nested-loops into `item_pkey` twice. The
loops keep the merge join's order, so no Sort sits under the Limit, and
`get_cheapest_fractional_path` prefers the loops' tiny startup
(80913..80929 for 100 rows). goopg's nested-loop paths carried no pathkeys
at all (`pathkeys=0` in DPPATH), so an ordered outer lost its order at the
loop. Under ORDER BY, the fractional election then had to pay a full Sort
for any nested loop, and the merge join over hash joins won.

## Change

`match_unsorted_outer` / `consider_parallel_nestloop` (joinpath.c) give a
nested loop `build_join_pathkeys(outer_path->pathkeys)`. The three goopg
producers now do the same: plain (`pathgen.go`), index/Memoize
(`joinpathsnli.go`) and partial. goopg's nested-loop executors stream the
outer row by row, so the ordering claim holds. `buildJoinPathkeys` already
returns nil for FULL/RIGHT.

## Results

- `census-diff-*.txt`: the top join is now a Nested Loop, as in PG, for
  Q31 and Q44 (SF0.25). Q65 diverges deeper at SF1. Nothing got shallower.
- `sf025-sweep.txt`: 96/96 with checksums, 5 plans changed.
  `runtime-notes.txt`: Q4's CTE-join order changed (6.7 s to 10.2 s,
  under the 2x threshold; neither order is PG's, and its census record is
  unchanged). Q23/Q58 moved with unchanged plans.
- `tpcds-fireset.txt`: no timeouts. TPC-H: 5/22, census identical,
  spotcheck PASS, the acceptance arm has 24 MATCH.

## Next gap (M0146-0005m)

Q44's lower `item` join is still hashed below the merge join. goopg's
nested-loop producers try only the outer rel's cheapest-total path, while
`match_unsorted_outer` tries every outer path. The all-nested-loop path over
the ordered merge join is never built.
