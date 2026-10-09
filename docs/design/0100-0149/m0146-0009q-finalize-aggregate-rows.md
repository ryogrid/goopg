# M0146-0009q — a Finalize aggregate's rows are the grouped rel's rows

Status: done (2026-10-04). Parent: M0146-0009. Filed by M0146-0005 slice
116.

## The divergence

At TPC-DS SF1, Q59's CTE `wss` is a `Finalize HashAggregate … rows=62646`
over a Gather of `Partial HashAggregate`s. Yet each `CTE Scan on wss` read
rows=6265, a tenth of that. PG reads 62640: `set_cte_pathlist` takes the
CTE subroot's final rel's rows. At SF0.25, where the CTE is a plain
HashAggregate, goopg's scan read 62646.

## Cause

A `*CTEScan`'s rows are `EstimateRows` of its body (`cardinality.go`). For
an `*Aggregate`, `estimateAggregate` re-estimates the distinct groups over
the node's own child. A Finalize node's child is the Gather of partial
states. The two grouping keys (`d_week_seq` from `date_dim`, `ss_store_sk`
from `store_sales`) sit above a Parallel Hash Join there. No column
statistics resolve, so `estimate_num_groups` falls to its default, a tenth
of the input.

EXPLAIN printed 62646 for the Finalize node itself because that line reads
the planning-time row stamp. Only the consumers that call `EstimateRows`
saw 6265.

PG never re-estimates a Finalize path. `create_partial_grouping_paths` and
the finalize paths (`./postgres/src/backend/optimizer/plan/planner.c`) all
take the grouped rel's `dNumGroups`, which `get_number_of_groups` estimates
once over the original input.

## Change

`estimateAggregate` answers for a Final-mode aggregate with its
`PartialSource`'s estimate. That is `estimate_num_groups` over the
Partial node's input, the original join, which is PG's dNumGroups. The
guard refuses a self-link or a Final-mode source.

## Effect

- **Repro.** A forced-parallel two-key grouping over a join, read twice as a
  CTE:
  - PG 18.3 prints rows=3432 for the Finalize aggregate and both CTE Scans;
  - goopg's scans read 2400 before and 3432 after.
- **Fire set (SF1).** Q44 and Q59 fired, with no timeout introduced.

  | category | before | after |
  |---|---|---|
  | parameterisation | 32 | 33 |
  | parallelism | 45 | 44 |
  | qual-placement | 11 | 10 |
  | rendering | 13 | 12 |

  - Matches are unchanged, and SF0.25 fired nothing.
  - **Q59:** the CTE Scans read 62646 (PG 62640), and the store joins 3759
    (PG 3758). The first divergence moved from qual-placement to
    join-method at the same depth. PG puts a Nested Loop over `d_1` with a
    Materialize above the two CTE joins, and goopg now puts a Hash Join.
    That is a cost election above the corrected rows (routed to
    M0146-0014).
  - **Q44:** the depth-1 join is now PG's Merge Join. The first divergence
    moved from join-method to parameterisation at the same node (B8,
    M0145-0008ag).
- **Other gates.**
  - The sweep passed 96/96 and the TPC-H arm 24/24; tpch-spotcheck and
    ea-ratchet passed.
  - Regress A/B over 9 cases (`aggregates`, `select_parallel`, `with`, …):
    only the known `join` row-order flip.

Test: `TestCTEScanOverFinalizeAggregateReadsItsRows` (executor) fails on
HEAD (2400 vs 3432). It uses the repro with parallel costs zeroed through
PlannerSettings.

Evidence: `analysis/m0146/m0146-0009q/`.

## Not done (ledgered)

- **Q59's residual.** The join-method election at depth 2 is now the first
  divergence, with PG's estimates beneath it.
