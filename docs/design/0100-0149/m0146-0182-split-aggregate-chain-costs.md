# M0146-0182 — the partial-aggregate split chain prints its paths' costs

Status: closed 2026-10-14.

## Problem

The partial-aggregate split prints PG's node chain, but its middle nodes
showed costs no path produced:

- **TPC-DS Q76 (sorted arm, SF0.25):** `Gather Merge (cost=37913.74..37982.60
  rows=3443)` sat over a Sort starting at the same 37913.74. PG prints
  `39026.66..40214.93 rows=10044`: the Sort's startup plus
  `parallel_setup_cost`, and `compute_gather_rows` — the per-worker groups
  times the parallel divisor.
- **TPC-DS Q2 (hashed arm):** the Gather started at 34529, above the
  Finalize HashAggregate's own startup (28326), so the parent looked cheaper
  than its child.

The Finalize itself was priced correctly (Q76's started at 38945), so the
planner chose on the right numbers; only EXPLAIN was wrong.

## Diagnosis

`createFinalizeAggPlan` and `createFinalizeAggSortedPlan`
(createplansimple.go) do not recurse through their chain's paths. They
build the whole chain in one step with `splitAggregate` /
`splitAggregateTransportSorted*` (parallel.go), which pairs the Partial and
Finalize nodes. `createPlanNode` stamps only the node it returns, the
Finalize. The Gather or Gather Merge, the worker Sort and the Partial
aggregate were never stamped, so EXPLAIN fell back to the legacy display
estimate (`DeriveLegacyDisplayCost`), a crude "parent costs at least its
child" figure.

## Change

Each arm now stamps the nodes it built from the paths they implement, with
`stampPlanCost`, the funnel every search-produced node goes through:

- **Hashed arm:** the Gather from the PathGather and the Partial from the
  partial PathAgg.
- **Sorted arms (`stampSortedSplitChain`):** the Gather Merge from the
  PathGatherMerge. Then, by arm:
  - presorted: the worker Sort and the Partial below it;
  - sorted-input: the Partial and the Sort below it.

  A chain that does not have the expected node kinds is left unstamped.

## Evidence

Files are in `analysis/m0146/m0146-0182/`.

- **Q76 at SF0.25:** Gather Merge 38954.59..40199.71, rows 10525 (PG
  39026.66..40214.93, rows 10044); Sort 37954.55; Partial HashAggregate
  37704.53..37755.45 (PG 37797.20..37837.70).
- **Q2 at SF0.25:** Gather 24155.47..27681.67, rows 32389 (PG
  27328.85..30846.58, rows 32311). The remaining gap is the Partial
  HashAggregate's own cost, 23155 against PG's 26328.
- **SF0.25 sweep:** 99 of 99 pass. Fourteen queries' plan text changed, all
  on cost figures of the Gather, Gather Merge, Sort and Partial lines; with
  costs stripped, the plans are identical.
- **Fire set:** match 76 / 64 and CATEGORIES-EXCL-MATCH are unchanged, as
  expected for a display fix.
- **TPC-H:** the acceptance arm is value-identical.
- **Test:** `TestExplainSplitAggregateChainCosts` covers both arms with PG's
  invariants. The boundary node costs at least its child plus
  `parallel_setup_cost` and returns more rows than one worker. The worker
  Sort starts after its Partial. A hash Finalize starts after its whole
  input. It fails with either arm's stamping removed.

## Not ported (ledgered)

- The Partial HashAggregate's cost runs below PG's: 23155 against 26328 on
  Q2. That is the hash-aggregate costing (`cost_agg` AGG_HASHED, the
  `hashAggEntrySize` work under banner item 4), not this chain.
