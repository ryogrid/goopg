# M0148\-0001j — ordered aggregates: no hashing, presorted transitions, state sharing

**Parent:** M0148\-0001 `aggregates.sql` regress coverage closure · **Status:**
landed 2026\-11\-03; closed (residue in M0148\-0001j1)

## Divergence

Three differences from PG 18.3 around aggregates with DISTINCT, ORDER BY or
WITHIN GROUP:

| query | PG | goopg (before) |
|---|---|---|
| `select ten, mode() within group (order by string4) from tenk1 group by ten` | `GroupAggregate` over `Sort (ten)`; groups print 0–9 | `HashAggregate`; groups print in hash order |
| `select my_avg(distinct one), my_sum(one) from (values(1),(3)) t(one)` | transition NOTICEs `1,1,3,3` | `1,3,1,3` |
| `select my_avg(one order by one desc), my_sum(one) from (values(1),(3)) t(one)` | four transition calls, `3,3,1,1` | two calls: both aggregates shared one state |

## PostgreSQL

- **No hashing.** `create_grouping_paths` sets `GROUPING_CAN_USE_HASH` only
  when `numOrderedAggs == 0` (planner.c). `preprocess_aggrefs` (prepagg.c)
  counts every aggregate with a non\-empty `aggorder` or `aggdistinct`.
  Ordered\-set aggregates always carry an `aggorder`. Presorting does not
  affect the count.
- **Presorted aggregates.** `adjust_group_pathkeys_for_groupagg` picks the
  aggregate sort list that covers the most aggregates (bestaggs). It
  appends that list to `group_pathkeys` and sets `aggpresorted` on those
  aggregates.
  - `build_pertrans_for_aggref` (nodeAgg.c) then needs no per\-group sort
    for them: an ORDER BY aggregate transitions per row like a plain one.
  - A DISTINCT aggregate gets an `EEOP_AGG_PRESORTED_DISTINCT_*` step that
    skips a repeated input.
  - Its calls therefore interleave with the group's plain aggregates, row
    by row.
- **State sharing.** `find_compatible_trans` (prepagg.c) shares a transition
  state only between aggregates whose transition function, arguments,
  FILTER, DISTINCT and ORDER BY all match.

## Change

- **Hash gate** (`groupingHashable`). The gate now refuses any grouping
  with a DISTINCT, ORDER BY or WITHIN GROUP aggregate.
  - The serial and gathered sorted arms accept the plain group\-key order
    as input when no presorted keys exist; the executor sorts each group's
    ordered\-aggregate inputs itself.
  - Before, goopg declined that sorted candidate and offered only the
    hashed one.
  - Grouping sets still always hash: goopg has no sorted grouping\-sets
    executor (residue).
- **Presorted marking** (`AggregateCall.Presorted`, `markPresortedAggs`).
  - `presortedAggSelection` returns bestaggs alongside the keys
    (`bestCoveringAggSet`).
  - After the grouping path is elected, an aggregate of bestaggs is marked
    only if the winner's input pathkeys start with the group keys (in any
    order) and continue with that aggregate's sort list.
  - PG can mark before path generation, because every grouping path then
    carries `group_pathkeys`. goopg's index\-ordered and reordered\-key arms
    can deliver another ordering; an unmarked aggregate still buffers its
    inputs, which is correct for any input order.
  - The mark goes on both the elected node and the winner's spec, because
    an ORDER BY above rebuilds the aggregate from the grouped rel's path.
- **Executor** (`applyAgg`). A presorted DISTINCT or ORDER BY user
  aggregate takes the per\-row transition path.
  - A presorted DISTINCT aggregate skips an argument tuple already seen in
    the group. The dedupe is keyed with `datumKey` like the buffered path,
    over the whole group rather than the previous row.
  - Shared\-state followers of a presorted leader copy its state, as for
    plain aggregates.
- **State\-sharing key** (`buildAggregate`). The key now includes every
  argument (Arg, Arg2, ExtraArgs) and the ORDER BY clause, with direction
  and null placement.

## Verification

- New tests:
  - `TestOrderedSetAggregateGroupingIsNotHashed`
  - `TestDistinctAggregateGroupingSortsWithPresortOff`
  - `TestPresortedAggregatesAreMarked` (including the bestaggs\-only case)
  - `TestSharedStateSlotSeparatesOrderBy`
  - executor `TestPresortedUserAggTransitionOrder`: call order 1,1,3,3; a
    DISTINCT repeat skipped; ORDER BY not shared with a plain call
- `TestUpperSplitOffersTheGatheredArmForANonSplittableAggregate` now
  expects the sorted gathered arm (a Sort over the Gather, or a Gather
  Merge) and no hashed candidate.
- Probe against a scratch PG 18.3 (grouped `count(distinct)` /
  `array_agg(distinct)`, `string_agg … order by … filter`, a grouped
  `percentile_disc` + `count(distinct)` mix, `enable_presorted_aggregate =
  off`, shared DISTINCT states, grouped DISTINCT + plain user aggregates).
  Plans, values and NOTICE order are identical, except for the residue
  below.
- Regress runner, HEAD → change, 14 suites: aggregates 605 → 567 lines,
  with both target hunks gone. groupingsets, partition\_aggregate,
  select\_distinct, window, subselect, polymorphism, create\_aggregate,
  tsrf, join, union, limit, stats\_ext and select\_parallel are
  byte\-identical.
- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass. The TPC gates show no plan change (fire\-set fires=none on all
  three corpora).

## Remaining (M0148\-0001j1)

- Grouping sets with an ordered aggregate: PG sorts (`GroupAggregate` with
  one `Group Key` per set); goopg hashes (`MixedAggregate`) because it
  has no sorted grouping\-sets executor.
- `DROP TYPE … CASCADE` does not drop functions whose signature uses the
  type. A later `CREATE FUNCTION` with that signature fails with "function
  already exists", and the stale function returns NULL.
