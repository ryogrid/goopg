# M0146-0099 — a merge join's input Sort is a plan node again

Status: done 2026-10-08 (20128cc7f). Parent: M0146-0097.

## Problem

TPC-DS Q44 at SF0.25 showed different plans in goopg and PG 18.3:

```
goopg:  Merge Join -> WindowAgg ...
PG:     Merge Join -> Sort (Sort Key: v11.rnk) -> Subquery Scan on v11 -> WindowAgg ...
```

The task was filed as "goopg derives a pathkey for `rank()`'s output". That
hypothesis was wrong:

- The merge path was costed with an explicit `PathSort` on each side, as
  `tryMergeJoinPath` builds it.
- `createMergeJoinPlan` then stepped over both sorts (`absorbMergeSort`)
  and emitted neither. The reason given was that goopg's `JoinAlgoMerge`
  operator sorts its inputs itself, so a Sort node would be doubled work.
- So no goopg plan ever printed a Sort directly under a Merge Join. Q44
  lost its two Sorts, and with them the two `Subquery Scan` nodes the
  Sorts keep.

## PG behaviour

- `create_mergejoin_plan` (createplan.c:4580-4650) puts a Sort over each
  side whose `outersortkeys` / `innersortkeys` are set. It uses an
  Incremental Sort when the side's subpath already has a presorted prefix.
- It plans that side's subpath with `CP_SMALL_TLIST` (:4526-4529). That is
  why `Subquery Scan on v11` survives `trivial_subqueryscan` under the
  Sort.
- `nodeMergejoin` streams its sorted inputs.

## Change

1. **The Sort goes back on.** `createMergeJoinPlan` still builds and
   narrows each side through its `PathSort`. That keeps the
   key-pair translation and `narrowMergeInput`'s sort-key coverage proof
   unchanged. It then re-emits the Sort above the narrowed node
   (`restoreMergeSort`), translating the PathSort's pathkeys onto the side's
   own layout (`joinInputs.outerLay/innerLay`, new). The Subquery Scan strip
   already treats a Sort as a pathtarget-regime consumer, so the wrapper PG
   keeps is kept.
2. **One sort, not two.** The executor's merge source still drains each side
   into runs. `mergeSortedSource.sortChunk` now skips the stable sort when
   the chunk is already in merge-comparator order (`sort.SliceIsSorted`):
   - Under the Sort node that check is one linear pass.
   - When the Sort's order and `compareMergeKeys` disagree (for example a
     NULL in a non-leading key, which the merge comparator places after
     every non-NULL key), the chunk is re-sorted, so results never depend
     on the two comparators agreeing.
3. **Parallel spine.** `drivingScanCrossesSort` steps over a merge join's
   own outer Sort. Each worker merges its sorted outer partition against the
   whole sorted inner, so a plain Gather above stays correct. That is the
   verdict the absorbed form produced, and
   `TestParallelMergeJoinIdentity` still finds its Gather.

## Verification

- **Tests.**
  - `TestCreateMergeJoinPlanEmitsSortChildren` replaces the absorb test: a
    PathSort side gets a `*Sort` in its own coordinates, and a presorted
    side gets none. Dropping the re-emission fails it.
  - `TestJoinFilterResolvesSubqueryColumnToSource` covers both cases:
    - a stripped `dn` prints `jr_addr.a_city`, as PG does;
    - a `dn` kept by an unread aggregate column prints `dn.bought_city`.
- **TPC-DS fire set** (SF0.25 and SF1). Q44 and Q78 change, and executed
  results are identical.
  - Q44 has PG's 4 Subquery Scans at both scales. At SF1 it matches PG
    node for node through the merge; the first divergence is the InitPlan's
    constant group key.
  - Q78 shows `Sort -> Subquery Scan` over its GroupAggregate inputs. goopg
    sorts all three because subquery leaves publish no pathkeys
    (M0146-0100), where PG merges two of them presorted.
- **TPC-H.** The plans contain no merge join, and the acceptance arm values
  are identical.
- **Regress A/B** (join, subselect, window, union, partition_join,
  select_parallel, aggregates). Result rows are identical.
  - PG-only Sort lines fell in every changed test: join 43→34, aggregates
    34→21, subselect 25→21, window 38→34, partition_join 105→99.
  - The new goopg-only Sorts sit under merge joins where goopg already
    chose a different join method or key order than PG.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS; only Q44 and Q78 change shape) and ea-ratchet all PASS.

## Deferred (ledgered)

- **Buffering.** goopg's merge operator still buffers each side it reads,
  so a Sort's rows are held twice; PG's nodeMergejoin streams. A streaming
  mode needs the Sort's comparator proven equal to `compareMergeKeys` per
  type (collation, cross-type keys, bpchar trim).
- **Incremental Sort.** PG uses an Incremental Sort for a merge input with a
  presorted prefix. goopg always emits a full Sort, as in regress
  aggregates' `btg` merge.
- **Material over a large sorted inner.** A sorted inner over work_mem that
  PG shields with a Material stays bare (the existing
  `mergeMaterialInner` row).
- **Subquery leaf pathkeys.** Subquery leaves still publish no pathkeys
  (M0146-0005bd's ledger row, filed as M0146-0100). The new Sorts make that
  visible where PG merges a presorted GroupAggregate.
