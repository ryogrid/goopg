# M0146-0193 — a partial merge join sorts its outer inside each worker

Status: closed 2026-10-14.

## Problem

Each `i_manufact_id IN (SELECT … FROM item WHERE i_category = …)` branch of TPC-DS Q33 planned differently at
SF0.25:

- PG 18.3 plans GroupAggregate → Sort → Hash Join over a Gather and a Hash of HashAggregate(item_1)
  (20527.79..20529.83);
- goopg planned GroupAggregate → Gather Merge → Sort → Parallel Hash Semi Join (20343.65..20357.10).

## Diagnosis

**PG side.** PG's partial pathlist dump from the OPTIMIZER_DEBUG build (`tmp/pgdbg`) holds ONE partial path for
the semi-join rel (store_sales, date_dim, customer_address, item, item_1):

- a Merge Join of the partial outer, explicitly sorted, with the unique-ified inner (UNIQUEPATH over item_1,
  i.e. JOIN_UNIQUE_INNER), costing 19543.70..19548.37 and sorted on `i_manufact_id`;
- with `enable_mergejoin = off`, the Parallel Hash Semi Join appears at 3502.44..19411.03.

The hash path is cheaper, but within STD_FUZZ_FACTOR and with worse pathkeys, so `add_partial_path` drops it
(`PATHKEYS_BETTER2` → `accept_new = false`). The plans PG can still build from the merge path and the serial
paths lead it to Sort over Hash Join.

**goopg side.** `tryPartialMergeJoinPath` (joinpathsparallel.go) declined any partial merge join whose outer or
inner needed a sort (vetoes M7/M8, "a sort under a partial is unmodelled"). The DPPATH trace for Q33 shows no
`join.merge.partial` producer at all. The Parallel Hash Semi Join therefore had no fuzz-dominating rival.

The decline predates the parts that make the shape executable. The node walks see through a Sort on the spine
(drivingScan's `*Sort` arm, R56). `partialPathDrivingKind` has a PathSort arm (M0146-0027). The executor's
`attachParallelScan` descends a `sortOp`. Gather Merge over per-worker Sorts already runs on all of these.

## Change

- `tryPartialMergeJoinPath` builds the explicit sorts that `create_mergejoin_plan` adds for `outersortkeys` /
  `innersortkeys`, as the serial twin `tryMergeJoinPath` does (`sortPathFor`).
  - The outer Sort runs per worker. It is priced on the partial path's per-worker rows and carries its worker
    count and parallel safety.
  - The inner is read whole by every worker, so its Sort is an ordinary one.
  - This is sort_inner_and_outer's partial arm (joinpath.c, `try_partial_mergejoin_path` with `outersortkeys`).
- The sort decline used to hide the jointype question. A partial merge join is now filed only for INNER, LEFT,
  SEMI and ANTI (`partialMergeJointype`, the producer twin of `mergeJoinIsPartialCapable`). PG never calls
  `try_partial_mergejoin_path` for FULL or RIGHT, and `assertPartialMergeJoinIsRunnable` panics on them.

## Evidence

Files are in `analysis/m0146/m0146-0193/`.

- **Q33 at SF0.25:** EXPLAIN (COSTS OFF) is identical to PG's, and the result set is identical (md5).
- **Fire set at SF0.25:** match 79 → 80 (Q33). Categories: join-order 17 → 16, aggregation-strategy 2 → 1,
  sort-strategy 7 → 6, parallelism 7 → 6. Q64 fires but keeps its record.
- **Fire set at SF1:** match 65 → 65. Q33 keeps its record. Q77 (COSTTIE-waived) gains join-method and
  parameterisation categories: join-method 12 → 13, parameterisation 12 → 13.
- **SF0.25 sweep:** 99 of 99 pass.
- **TPC-H:** the acceptance arm is value-identical; spotcheck passes.
- **Regress A/B:** select_parallel, aggregates, subselect and partition_join diff identically to HEAD.
  - join differs only in the row order of a Hash Right Join's unmatched rows (`parent left join (… child) ss`).
  - That order flips across runs of HEAD as well: 3 of 4 runs differ from PG.
- **Tests:**
  - `TestPartialMergeJoinSortsItsOuterPerWorker` (postmaster) checks PG 18.3's
    `Merge Join → Sort → Parallel Seq Scan` subtree under the Gather, and the values.
  - `TestPartialMergeJoinSite1SortsUnsortedOuterPerWorker` replaces the old decline test.
  - `TestPartialMergeJoinRefusesRightAndFull` pins the jointype gate.
  - The C-19f hash-join executor tests turn merge joins off. On their fixture the sorted partial merge join is
    within the fuzz of the hash join with better pathkeys, so add_partial_path keeps it, as PG would.

## Not ported (ledgered)

- goopg has no merge semi join at all, serial or partial (`mergeu` veto M-uniq). PG plans `Merge Semi Join` with a
  per-worker-sorted outer for an IN subquery when hashing is off.
- `enable_gathermerge = off` does not stop the upper aggregate arms from filing a Gather Merge. A fixture with that
  setting plans Finalize GroupAggregate → Gather Merge where PG plans Sort → Gather.
- SF1 Q33 keeps its record; SF1 Q77's record grew.
