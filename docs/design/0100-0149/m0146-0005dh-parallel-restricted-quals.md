# M0146-0005dh: parallel-restricted quals and CTE scans stay out of the workers

Status: landed 2026-10-02 (banner item 3, M0146-0005 slice 115).

## Defect

PG never runs two things inside a parallel worker, and goopg did run both:

- **A parallel-restricted expression.** A correlated SubPlan reads its
  outer references through PARAM_EXEC Params, so `subplan->parallel_safe`
  is false and `max_parallel_hazard_walker` reports the qual as
  restricted. `set_rel_consider_parallel` (allpaths.c) then gives the
  relation no partial path, and PG scans it serially. TPC-DS Q41's
  `((SubPlan 1) > 0)` on `item i1` ran in a Parallel Seq Scan under a
  Gather Merge.
- **A CTE scan.** "CTE tuplestores aren't shared among parallel workers, so
  we force all CTE scans to happen in the leader" (the RTE_CTE arm). TPC-DS
  Q2 hashed two `CTE Scan on wswscs` build sides under a Gather Merge.

Both came from the Gather post-pass (`MaybeAddGather`, called by the
server after planning), not the search. Its `findPartialSubtree` only asks
whether the subtree bottoms out in a driving scan. In Q41 the SubPlan
conjunct is held above the search, outside the problem's clause list, so
the search's `relConsiderParallel` never saw it either.

## Change (internal/optimizer/parallel.go)

- `maybeAddGatherInner` refuses a partial target in two cases. In both,
  the statement level stays serial, and the CTE bodies and sublinks still
  get their own verdicts through `graftTop`.
  - `partialSubtreeExprsParallelSafe` is false: some expression the target
    evaluates fails `isParallelSafeExpr`, the same predicate the search's
    consider_parallel flags use. Sublink bodies are judged through their
    SubPlan.
  - `partialSubtreeScansCTE` is true: the target's own plan holds a
    non-inlined CTE scan. An inlined CTE is a subquery RTE upstream, so its
    body is walked as part of the level.
- `findPartialSubtree` stops at a parallel-restricted Filter that sits
  directly on a base scan, because that Filter is the relation's
  baserestrictinfo. Without the stop, the Gather slid below the qual, onto
  the bare scan. PG still allows a restricted qual above a join, so there
  the walk continues and the inputs may go parallel.
- considerparallel.go: `setBaseRelConsiderParallel` also walks held clauses
  that reference only the relation (`heldBaseQualsParallelSafe`). This is
  PG's baserestrictinfo check for quals goopg keeps in the search's clause
  list rather than in the leaf.

A first attempt put the check in `drivingScan`. That walk is also the
structural assertion of the PathGather lowering, so a search-chosen Gather
lost its driving scan and the backend panicked on Q75 (the sweep caught
it). The check now lives only in the post-pass.

## Verification

`TestGatherPostPassKeepsParallelHazardsSerial` (optimizer) covers five
cases:

- a correlated-SubPlan qual on its own and under an aggregate, which stay
  serial;
- a hash join over a materialized CTE scan, which stays serial;
- a plain qual and a plain hash join, which are still gathered.

Disabling either check fails its cases. Disabling the qual check, the
`count(*)` case would run the SubPlan in workers.

Probe on an SF0.25 clone: Q41 and the main body of Q2 plan without a
Gather, as PG does, and an ordinary `count(*) … where ss_quantity > 5`
still gathers.

Measurements:

- PLAN-PARITY match: SF0.25 38 → 39 (Q41 now matches); SF1 28 → 28.
- Text-identical: SF0.25 34 → 35.
- CATEGORIES-EXCL-MATCH SF0.25: join-order 53 → 52, parallelism 35 → 34,
  sort-strategy 31 → 29. SF1: Q59 drops its sort-strategy and parallelism
  categories.
- Regress A/B: `join` moves 2 lines toward PG (a top Sort, a correlated
  EXISTS no longer in workers). `select_parallel`, `write_parallel`,
  `subselect`, `with` and `aggregates` are unchanged.
- Gates pass: units, spotcheck, sweep 96/96, arm, fire set (Q2, Q41 / Q2,
  Q59), ea-ratchet.

## Left open (ledgered)

- PG parallelises Q2's CTE body (partial HashAggregate over a Parallel
  Append); goopg's stays serial. That is the PARTIAL family (M0146-0027 /
  M0140-0006).
- The SubPlan's cost is still not charged to the qual (M0146-0005di).
- `heldBaseQualsParallelSafe` has no corpus case that reaches it yet: the
  Q41 clause is held outside the problem entirely.
