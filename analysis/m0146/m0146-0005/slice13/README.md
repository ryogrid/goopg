# M0146-0005 slice 13 (M0146-0005n): join paths drop useless pathkeys

PG's `build_join_pathkeys` returns
`truncate_useless_pathkeys(root, joinrel, outer_pathkeys)` (pathkeys.c). A
join path keeps its outer's ordering only as far as one of these can use
it:
- a later merge join (`pathkeys_useful_for_merging`: the key's equivalence
  class still has a member outside the joinrel, in the right direction);
- the query's ordering (`pathkeys_useful_for_ordering`).

goopg kept the outer's full ordering. That is harmless while nested loops
see only the cheapest outer, but it floods add_path once they see every
outer (the M0146-0005m attempt, `../recon-0005m/`).

## Change

- `pathkeys_useful.go`:
  - `pathkeyUsefulnessFor` computes each joinrel's merge-useful expressions
    once, in `makeJoinRel`, from the search's equijoin clauses grouped by
    equivalence class (syntactic pathkeys);
  - `truncateUselessPathkeys` keeps the longer of the merge prefix
    (`right_merge_direction` included) and the ORDER BY prefix.
- `buildJoinPathkeysFor(joinrel, …)` applies it in all four in-search
  callers (merge, partial merge, plain nested loop, index/partial nested
  loop).
- The plan-level caller (`upperorderedinput.go`) keeps the plain rule.
- `setop_join_promotion_test.go`: its "equi-join still a Filter" check
  greps only `Filter:` lines now. A nested loop's `Join Filter:` is the
  promoted join condition the test's own R36 note accepts, and the new
  plan renders it that way.

## Results

- `census-diff-*.txt`: only Q65 changes category, at the same depth
  (qual-placement → join-method). Nothing moves shallower.
- `sf025-sweeps.txt`: 96/96 twice. `q31-timing.txt`: Q31's flagged 2.5x
  is first-run variance; warm runs are equal.
- `tpcds-fireset.txt`: no timeouts. TPC-H 5/22, census identical,
  spotcheck PASS, the acceptance arm has 24 MATCH.

## Not ported (ledgered)

The grouping, distinct and set-op arms of `truncate_useless_pathkeys`. The
search keeps only the chosen `query_pathkeys`.
