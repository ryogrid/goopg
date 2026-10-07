# M0146-0096 — the PH4 batch veto is gone

Status: done 2026-10-08 (0f3165f64). Parent: M0146-0090. Evidence:
`analysis/m0146/m0146-0096/`.

## Problem

`addParallelHashJoinPath` refused any Parallel Hash path whose build would
not fit one batch (`PH4-batches`), either as a whole under the combined
budget or per participant under hash_mem. The refusal stood in for
parallel batching that the executor did not have.

PG has no such refusal. `initial_cost_hashjoin` prices the batch I/O, and a
multi-batch Parallel Hash is elected like any other path; regress
join_hash's "good" case and its `join_bar` rescan case both plan one.

## Change

### Planner

Since M0146-0090 and M0146-0095 the executor batches a spilled share for
every join type, so the veto is removed. `hashJoinCost` already charges the
spill I/O through the same per-participant geometry the executor uses.

### Executor gap exposed

With the veto lifted, regress join_hash failed with "participants chose
different bucket counts (2^11 vs 2^12)". Shares size their tables
privately, so two shares can disagree on the bucket count and therefore
take their batch bits from different hash bits. `mergeSpilledParts`
required them to agree.

It now re-routes every row from its stored hash under one geometry, the
first spilled share's. Every participant routes its probe rows by that same
geometry.

- **Batch-0 rows.** A spilled row that the merged geometry assigns to batch
  0 goes into the share's in-memory maps (`sharedHashBuild.insertKeyed`,
  using `lazyHashInsertKeyed`'s lane rule).
- **Lanes.** Once any share holds string-keyed rows, `merge` folds the
  int-lane table into the string lane (`demoteIntHash`'s rule), so a probe
  never misses rows filed under the other key representation.

## Verification

- **Unit test.** `TestParallelHashMergeAcrossBucketCounts` merges shares
  built at 2048×2 and 4096×4.
  - Every one of 400 keys must survive exactly once.
  - A key may sit in memory only if the merged geometry routes it to batch
    0; otherwise it must sit in its batch's file.
  - The previous merge fails this test with the bucket-count error.
- **A/B against HEAD.**
  - TPC-DS fire sets (SF0.25 and SF1): no changed query.
  - TPC-H plan capture (estimate-audit arm, plan-only): byte-identical,
    costs included. TPC-H elects no Parallel Hash, so the veto never decided
    a corpus election.
  - TPC-H acceptance arm: values identical, timings within noise.
- **Regress.**
  - join_hash: 322 → 310 diff lines, removals only. The multi-batch
    Parallel Hash Join and the `join_bar` Parallel Hash now print PG's
    plans.
  - select_parallel is identical; join differs only in its known row-order
    flap.
- **Race and gates.** `go test -race` on the TestParallelHash family is
  clean. units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (99/99 shapes unchanged) and ea-ratchet all PASS.

## Deferred (ledgered)

- **Batch growth.** PG grows a Parallel Hash's batch count cooperatively,
  against the combined budget, while the build runs. goopg's shares grow
  privately and are repartitioned once at the barrier.
- **Later batches of a build-filling join.** These run in the sweeper alone
  (M0146-0095).
