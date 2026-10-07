# M0146-0090 — a Parallel Hash build that outgrows hash_mem batches

Status: done 2026-10-07 (1a3f12277). Parent: M0146-0002. Evidence:
`analysis/m0146/m0146-0090/` (regress join_hash diffs before and after).

## Problem

Under M0146-0002 every Gather participant builds its claimed share of a
partial inner, then all shares merge into one shared table behind a build
barrier. A share that grew past one batch failed the query:

> parallel hash join: a participant's build exceeded hash_mem and spilled;
> parallel hash batching is not supported

The planner's PH4 veto keeps Parallel Hash off inners estimated to spill,
so reaching this error takes an underestimate. Regress join_hash has two:

- `bigger_than_it_looks`: 20000 rows, reltuples lowered to 1000. M0146-0064
  made that lie reach the planner.
- `extremely_skewed`.

PG answers 20000 for both.

## PG behaviour

- PG's shared hash table grows its batch count while the build runs
  (`ExecParallelHashIncreaseNumBatches`, nodeHash.c), and the participants
  repartition cooperatively.
- Batches after 0 are then processed with a barrier per batch
  (`PHJ_BATCH_ELECT` … `PHJ_BATCH_SCAN`).
- For a join that fills its build side, the last participant to leave a
  batch's probe runs that batch's unmatched scan.

## Change

goopg's shares build privately, each under its own growth
(`hashBatchState.increaseNumBatches`). The repartitioning moves to the
barrier.

- **Collecting.** `parallelHashBuild.finish` keeps every published share. A
  spilled share comes with its batch state, detached from the operator:
  its maps hold its batch 0 and its files hold its other batches.
- **Merging.** The arrival that completes the barrier calls `mergeParts`,
  under the build mutex, while every other participant waits on `done`.
  Shares that all fit merge map by map, as before. If any share spilled,
  `mergeSpilledParts` builds one batched table:
  - The batch count is the largest any share reached. All shares use the
    same `bucketBits`; a mismatch is an XX000 error.
  - A share's batch-0 rows that the merged count routes elsewhere are
    written to the merged inner files.
  - Every row of a share's own files is re-filed under the merged count.
  - Both moves carry the stored hash and canonical key (the E-14 keyed
    frame), so no key expression is evaluated.
  - This is legal for the same reason a serial doubling is: the batch
    number is the low bits of `hash >> bucketBits`. A row of batch k ≠ 0
    therefore never lands in batch 0, and a row that would is an XX000
    error rather than a lost row.
- **Probing.** The merged files are closed and frozen into the same
  `sharedBatchDesc` the leader prebuild publishes (E-09a/b).
  - Each participant adopts batch 0's maps and derives a private batch state
    from the descriptor (`newParticipantBatchState`).
  - It probes batch 0 and spills its own probe rows for later batches.
  - It loads batch k through the descriptor's shared load slots.
  - Every probe row is seen by exactly one participant, and every batch's
    full build side is reloaded by whoever probes it, so the result is the
    serial join's.
- **Cleanup.** Gather and GatherMerge Close unlink the merged files
  (`releaseParallelHashBuilds`).

### Refused: joins that fill the build side

RIGHT, FULL, RIGHT SEMI and RIGHT ANTI still refuse a spilled share, with
a narrowed error (`… of a join that fills its build side is not
supported`). A build row of batch k can be matched by any participant's
probe rows. The unmatched sweep for that batch therefore needs every
participant's matched bits for it, which is PG's per-batch barrier.
M0146-0005dj's merge-at-probe-EOF covers batch 0 only. Ledgered.

## Verification

- **`TestParallelHashSpilledBuildMatchesSerial`** replaces
  `TestParallelHashSpillFailsLoudly`.
  - INNER, LEFT, EXISTS and NOT EXISTS with work_mem 16 kB batch (nbatch
    2–4) and return the serial rows.
  - The published rows equal the inner's non-NULL keys.
  - RIGHT and FULL refuse with the narrowed error.
  - No spill file is registered after the Gather closes.
  - Mutation: skipping the re-file of the shares' files returns 13376 rows
    against serial 58721.
- **Regress join_hash A/B** (HEAD build vs this one):
  - Both errors are gone, and both `count(*)` results match PG (20000).
  - The batch-count checks that follow fail on gaps in the
    `hash_join_batches()` helper itself: RETURNS TABLE column references
    and `json_array_elements` (M0134-0037).
  - Changed lines went from 326 to 322.
- **Regress select_parallel and join A/B:** select_parallel is identical.
  join differs by one row's position in an unordered result.
- **Gates:** TPC-H spotcheck and acceptance arm PASS. The fire set changed
  no plan. The SF0.25 sweep has 99/99 shapes unchanged. ea-ratchet PASS.

## Deferred (ledgered)

- **Build-filling joins with a spilled share** (filed as M0146-0095):
  the per-batch matched-bit merge.
- **The PH4 veto** (filed as M0146-0096). It keeps Parallel Hash off
  inners estimated to spill, where PG costs the batches and elects
  Parallel Hash anyway. For joins that do not fill the build side the
  executor can now run that shape, so the veto is goopg-only there.
  Retiring it changes plan elections and needs its own corpus A/B.
- **Memory bound.** The merged batch 0 is the union of the shares' batch-0
  rows under the larger count, with no further growth after the merge.
  PG grows the shared table while it builds, against the combined budget.
  A share that reached its cap or froze its growth (the
  `extremely_skewed` shape) keeps that batch's rows resident, as the
  serial path does.
