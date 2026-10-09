# M0146-0005m (not landed): nested loops over every outer path

`every-outer-path.wip.patch` ports `match_unsorted_outer`'s outer loop.
`addNLIPaths` and `addNestLoopPath` build nested loops over every
unparameterised path of the outer rel, where goopg used only the
cheapest-total one. JOIN_UNIQUE_OUTER keeps its single unique-ified outer.
Unit tests included.

## What it fixed

TPC-DS Q44 plans exactly as PG does: two ordered nested loops into
`item_pkey` over the rnk merge join, with no Sort under the Limit.

## Why it is not landed

The gates pass (sweep 96/96, total runtime −4.2%, fire set without
timeouts), but the census regresses (`census-diff-*.txt`):
- SF0.25 Q13: depth 4 → 1;
- SF0.25 Q48: depth 2 → 1;
- SF1 Q91: loses its MATCH.
Q16 and Q26 go deeper. In each regression a Merge Join appears where PG
has a nested loop. For Q91 the merge join is dearer in total than the
nested loop it replaced.

`q48-top-joinrel-dppath.txt` shows the mechanism: the top joinrel now keeps
a dozen ordered nested-loop paths (`pathkeys=1`, totals 24000–89000), which
add_path retains for their orderings. PG never keeps these. Its
`build_join_pathkeys` ends with
`truncate_useless_pathkeys(root, joinrel, outer_pathkeys)` (pathkeys.c):
an ordering survives only as far as a later merge join
(`pathkeys_useful_for_merging`) or the query's ORDER BY
(`pathkeys_useful_for_ordering`) can use it. goopg's `buildJoinPathkeys`
returns the outer's keys untruncated, which slice 12 (M0146-0005l) made
visible on nested loops and this patch multiplies.

## Prerequisite

M0146-0005n: port `truncate_useless_pathkeys` into `buildJoinPathkeys`,
for merge joins too. Then re-apply this patch and re-measure.
