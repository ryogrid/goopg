# M0146-0003a: a subquery's aggregate splits over the Gather the search placed

## PG behaviour

`create_partial_grouping_paths` / `gather_grouping_paths`
(postgres/src/backend/optimizer/plan/planner.c) run at every query level.
A subquery in FROM whose input has a partial path plans its aggregate as
`Finalize Aggregate → Gather → Partial Aggregate`, even when that subquery
is one input of an outer nested loop. TPC-DS Q90 joins two such subqueries
(`am`, `pm`), each a `count(*)` over a parallel join.

## goopg before

The upper-rel split producer (`addPartialAggSplitPath`) refused whenever
the planning scope was not the statement's top level
(`ParallelStatementOK`, trace `gate=statement`). In a subquery leaf the
join search still placed a Gather, so Q90 planned `Aggregate → Gather →
…`: one parallel section, with the aggregate done serially above it.

## Change

In a nested scope the producer now splits when both hold:
- the aggregate's input already carries a Gather that
  `gatherToUnwrapForPartialAgg` can unwrap. Aggregate(Gather(X)) becomes
  Finalize(Gather(Partial(X))), which moves the aggregation below the
  existing Gather and adds no parallel section;
- the input has no escaping outer reference. A correlated body reads its
  outer values as PARAM_EXEC params, which are parallel-restricted in PG,
  and the unnest pass decorrelates its aggregate as a plain Aggregate
  (TPC-H Q2's scalar `min(ps_supplycost)` body; two existing tests pin it).

Without an existing Gather the statement-level refusal stands, because a
new Gather there could land under another parallel-aware node.

## Results

- TPC-DS Q88 and Q90 match PG with no structural divergence at SF0.25;
  Q90 also at SF1, where Q88 moves from depth 7 to 10.
- Category records (fire-set CATEGORIES-EXCL-MATCH):
  - SF0.25: sort-strategy 61 → 55, parallelism 69 → 63,
    aggregation-strategy 36 → 34, join-method 47 → 45;
  - SF1: similar reductions.
- Row counts, TPC-H (values and census) and the regress cases are
  unchanged.
- Evidence: `analysis/m0146/m0146-0003a/`.

## Not covered (ledgered)

- A nested scope whose input has no Gather still cannot get a partial
  path of its own. PG would build one per query level.
- The post-pass split (`splitAggregate`, parallel.go) keeps its own gates.
- M0146-0003's row-emitting partial rows (M0141-S3 → S6) remain open.
  They are needed for PG's `Finalize GroupAggregate → Gather Merge → Sort
  → Partial HashAggregate`.
