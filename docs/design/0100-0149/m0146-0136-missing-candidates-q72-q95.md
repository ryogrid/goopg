# M0146-0136 — recon: missing candidates in TPC-DS Q72 (SF1) and Q95 (SF0.25)

Status: done 2026-10-09 (recon; no code). Parent: M0146-0014a.

The M0146-0014a sweep noted two plans that looked like candidates goopg
never generates:

- **Q72 at SF1** hash-joins `d3` (+3229, 548 rows against PG's 2) where a
  5-probe nested loop would cost about 61.
- **Q95 at SF0.25** stays serial (8450) although its own parallel estimate
  is about 7.7k.

Both were DP-traced on private clones: `tmp/c20a/data-sf025` for SF0.25,
and a throwaway copy of `bench/tpcds/runtime_goopg/data` for SF1. The
excerpts are in `analysis/m0146/m0146-0136/`.

## Q72 (SF1): the sub-problem leaf's rows are re-estimated

Q72's explicit JOIN chain exceeds `join_collapse_limit` (8). Both PG and
goopg search the first eight relations as one problem, then join the
result to `d3` and `promotion` in a second, three-relation problem.

- **The search leaves are the same; the row counts differ.** In goopg's
  second problem the `?0` leaf, which is the eight-relation searched
  subtree, enters with `rows=1623`. Its own searched rel, the Gather whose
  `rows=5` the plan prints, has 5. PG's lower joinrel carries its own size
  into the upper problem (5).
- **The `d3` candidate is generated but mis-priced.** goopg does build the
  `d3` parameterised probe (`index.parameterised relids={1}
  reqouter={0}`, 6.27 per probe) and the nested loop over it
  (`nestloop.index`). But it prices that loop for 1623 outer rows, at
  71221.82, so the hash join (64211.41) wins. At 5 rows the loop costs
  about 61046 and wins, as in PG. The 548 (541) rows of `{?0+d3}` come
  from the same inflated input.
- **Cause.** `initialRelRows` (joinsearch.go) re-derives a sub-plan leaf's
  rows with `EstimateRows` over the built tree. That is the
  "pathlist-and-rows collapse" `relfromjoinlist.go` documents at the
  boundary. When the leaf is a searched tree,
  `searchedJoinInputRelOf(leaf).Rows` carries the search's own estimate.
- **Filed as M0146-0141.** The change affects every query split by
  `join_collapse_limit` and every searched subquery leaf, so it needs its
  own fire set.

## Q95 (SF0.25): no ordered-aggregate query pathkeys

PG drives Q95 from `Gather Merge (Sort ws1.ws_order_number) ->` a partial
`ws1 ⋈ web_site ⋈ date_dim` join. That order runs through the semijoins
into `Aggregate (count(DISTINCT ws1.ws_order_number))` with no Sort.

- **Why PG has the candidate.** `standard_qp_callback` builds
  `group_pathkeys` from the ordered aggregate
  (`adjust_group_pathkeys_for_groupagg`) and makes them `query_pathkeys`.
  `generate_useful_gather_paths` then sorts partial paths on them.
- **Why goopg lacks it.** goopg's `deriveQueryPathkeys` has no
  ordered-aggregate arm. With no GROUP BY, the query pathkeys are empty,
  and the seven-relation problem files no `gather.merge.sort` at all.
- **The serial pick itself is faithful.** At the top level goopg does
  file the Gather-outer candidate: total 84297.99, startup 1092.86,
  against the serial path's 85104.24 and 92.86. The totals are within 1%,
  so `add_path` keeps the better startup, exactly as PG's
  `compare_path_costs_fuzzily` would. The "own parallel estimate ≈ 7.7k"
  is the lower Gather; it is not lost, it ties.
- **Filed as M0146-0142.** M0146-0027 / 0005ag already handle ordered
  aggregates at the grouping step (`presortedAggKeysOrAbsent`, the
  Agg → Sort / Gather Merge arms). The missing piece is upstream of the
  search, in its `query_pathkeys`.

## Filed

- **M0146-0141** — a searched sub-problem or subquery leaf enters the
  enclosing search with its searched rel's rows (Q72 SF1).
- **M0146-0142** — `deriveQueryPathkeys`' ordered-aggregate arm,
  `adjust_group_pathkeys_for_groupagg` (Q95 SF0.25).
