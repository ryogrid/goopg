# Design 0063-0002 — Q5 six-table MHJ probe throughput

| field      | value |
| ---------- | ----- |
| status     | draft |
| date       | 2026-05-07 |
| milestone  | 0063 — TPC-H residual long-tail v2 |
| supersedes | — |

## 1. Problem

Q5 cancels at the 600 s `--cancel-after` deadline on TPC-H SF=1.
Cancel propagation itself is responsive (M0062-0001 added
`ctx.Err()` to `multiHashJoinOp.initStepHelper` and
`advanceFrom`); the deadline expires because the query
genuinely needs more than 600 s under the current plan.

Q5 SQL (`internal/testutil/tpch/tpch.go:5`):

```sql
SELECT n_name, sum(l_extendedprice * (1 - l_discount)) AS revenue
  FROM customer, orders, lineitem, supplier, nation, region
 WHERE c_custkey  = o_custkey
   AND l_orderkey = o_orderkey
   AND l_suppkey  = s_suppkey
   AND c_nationkey = s_nationkey
   AND s_nationkey = n_nationkey
   AND n_regionkey = r_regionkey
   AND r_name      = 'ASIA'
   AND o_orderdate >= date '1994-01-01'
   AND o_orderdate <  date '1994-01-01' + interval '1 year'
 GROUP BY n_name
 ORDER BY revenue DESC;
```

Cardinality:

| table    | rows |
| -------- | ----:|
| region   |    5 |
| nation   |   25 |
| supplier | 10 K |
| customer | 150 K |
| orders   | 1.5 M |
| lineitem |   6 M |

Live EXPLAIN (post-M0062 sweep) shows a 6-table chained
`MultiHashJoin` with all six tables as plain `Seq Scan` inputs.
The probe-side row count post-pruning is approximately
`#orders-in-1994 × avg-lineitems-per-order ≈ 230 K × 4 ≈ 920 K`
— each row threading the 5-step hash chain. At ~µs per step,
920 K × 6 ≈ 5.5 s of pure step work; the rest of the 600+ s is
in eval cost amortised across non-matching probes that descend
deep before backtracking.

## 2. Hypothesis

Three contributory factors:

1. **Build-order suboptimal.** The 6-table MHJ might be
   scanning `lineitem` (6 M) on the probe side when probing
   from `orders` (post date-filter ≈ 230 K) would be cheaper.
   Or the small-dimension build order (`region → nation`)
   isn't being pinned tightly enough.
2. **No index-driven inner.** `supplier` has `supplier_pk`,
   `customer` has `customer_pk`, `orders` has `orders_pk`.
   The `nation_pk` and `region_pk` are also present. Q14 /
   Q9 use NLI for similar shapes; Q5 doesn't because the
   chain is single-MHJ. Switching one or two of the leaves
   to an `IndexScan` driven by the joined-side keys would
   dramatically cut per-probe-row work for the small
   dimensions.
3. **Per-step filter pruning gap.** The MHJ already has
   `partitionFilters` (M0043-0002), but the date predicate
   on `orders` may not be promoted to a step filter that
   prunes early.

## 3. Critical code paths

| Path | File:line |
| ---- | --------- |
| MHJ build / probe planning | `internal/planner/bushy.go::tryBushyDP` and chain selection (~line 600-1000) |
| MHJ leaf selection / OID-sort | `internal/planner/bushy.go::collectMultiHashTables` (~line 920-960) |
| Per-step filter classification | `internal/executor/multi_hash_join.go::partitionFilters` (line 260-330) |
| Small-dimension override | `internal/planner/pushdown.go::IsSmallDimensionSide` |
| NLI rewrite (already exists for chain-step shapes) | `internal/planner/nl_index_join.go::rewriteJoinsToNLI` |

## 4. Proposed change

A profile-driven, multi-step approach. Each step is independently
testable; ship in order until Q5 fits inside the budget.

1. **Profile.** Run Q5 with `pprof` (CPU + alloc) at
   `cancel-after = 1200s`. Identify whether the bulk is in
   `initStepHelper`, `advanceFrom`, `evalFilters`, or
   per-row decode. Output: `bench/tpch/pprof/q5-baseline.pprof`.

2. **Step-filter promotion.** Ensure the date predicate on
   `o_orderdate` is classified as a step-time filter at the
   `orders` step in `partitionFilters`, so non-matching
   orders are pruned before `lineitem`-side cartesian
   expansion.

3. **NLI for `region` and `nation`.** The two smallest
   tables (5 / 25 rows) are perfect NLI inner candidates
   given their PK indexes. When they're inside an MHJ
   chain, the chain rewriter could detach them as NLI
   inners in front of the chain. Requires extending
   `rewriteJoinsToNLI` to detect intra-MHJ candidates, OR
   teaching the bushy DP to emit `Filter →
   NestedLoopIndexJoin → MultiHashJoin` shapes.

4. **(Stretch) `customer` and `supplier` as NLI inners.**
   Both have PKs. If steps 1–3 don't bring Q5 inside 600 s,
   promote these too.

## 5. Acceptance

- Q5 OK in < 600 s on TPC-H SF=1 with row-count parity (the
  canonical Q5 result is 5 rows — one per non-ASIA-region
  nation; we accept any 5-row positive result that matches
  PostgreSQL's output for this dataset).
- A pprof artifact (`bench/tpch/pprof/q5-fixed.pprof`)
  demonstrating the per-step distribution after the fix.
- `go test ./...` PASS, including any new MHJ planner tests
  that pin the new step-filter promotion or NLI shape.

## 6. Risks & rollback

- Mis-classifying a step filter could drop rows the user
  expects. Mitigation: targeted tests in
  `internal/planner/bushy_test.go` + the existing
  parity-vs-PG infrastructure.
- NLI inside an MHJ chain is a structural change. The
  `enable_nestloop_index` GUC kill-switch reverts the
  shape if a regression appears.

## 7. Out of scope

- Distributed or parallel execution.
- The Q5 ORDER BY revenue DESC final sort (it's only 5
  rows post-aggregate).
