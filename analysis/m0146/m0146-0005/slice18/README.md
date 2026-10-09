# M0146-0005 slice 18 (M0146-0005s): range estimates take PG's eq_selec

Found while tracing why PG's INTERSECT smaller-input swap (0005r, not
landed) flipped TPC-DS Q14's cross_items arms the wrong way: goopg's
web_sales arm estimated 1737 rows where PG has 2643, because
`d_year BETWEEN 1999 AND 2001` estimated 705 rows (PG 1049, actual 1096).

## Change

- `histogramOpSelectivity` (selectivity.go) follows
  ineq_histogram_selectivity: it estimates `x <= c` from the bin the probe
  lands in, rescales the first bin by eq_selec, subtracts eq_selec for `<`
  and `>=`, and flips for `>`/`>=`. eq_selec is `histogramEqSel`,
  1/(ndistinct - #MCV). `rangeOpSelectivityStats` gains the relation's
  tuple count to resolve a relative ndistinct.
- `addSortedSetOpPath` (windowsetoppaths.go) takes a hashed DISTINCT arm's
  Sort + Unique form (`sortedSetOpArm`), which is PG's
  get_cheapest_path_for_pathkeys over the arm rel. Without it the estimate
  change moved Q38/Q87's first arm across the 1% fuzz to HashAggregate and
  both queries lost 0005q's sorted SetOp (`census-diff-sf025-eqsel-alone.txt`,
  `q87-arm1-distinct-election.txt`).

## Results

- `d_year-range-estimates.txt`: `>=` and `>` now differ by one eq_selec, as
  in PG; the BETWEEN estimate is 1069 rows.
- SF0.25 first-divergence census: identical to HEAD.
- SF1: Q38 depth 2 -> 3 and Q87 depth 1 -> 4, both now PG's sorted SetOp
  (HEAD's SF1 arms elected HashAggregate).
- TPC-H census identical; spotcheck PASS; acceptance arm 24 MATCH; sweep
  96/96; fire set PASS with no timeouts.

Not ported (ledgered): the hundredth-of-resolution cutoff clamp and
get_actual_variable_range's endpoint refresh.
