# Parallel hash build — what goopg already has, and why the obvious extension is a correctness hazard

**Status:** implemented — narrow fix landed (measurably neutral), wider extension declined for correctness
**Date:** 2026-08-30
**Branch:** `perf-opt-take5`
**Baseline:** `2abf67421`
**Cluster:** TPC-H SF=1, `bench/tpch/runtime_goopg/data`, S-cold

---

## 1. The premise was wrong: a shared build already exists

The task was to *implement* a PostgreSQL `Parallel Hash` equivalent. goopg
already has a shared-build mechanism, and it is deliberately simpler than
PostgreSQL's, because goopg's workers are goroutines in one address space:

| | what it does | where |
|---|---|---|
| **P8 — shared build** | The build side is built **once** and every worker probes the same table **by pointer**. Replaces PG's DSA + barrier with a struct and a map read. | `prebuildSharedHashJoins`, `sharedHashBuild`; reached from `gatherOp.Open` (via `prebuildHashJoins`) and `gatherMergeOp.Open` |
| **M0129-S4.1 — cooperative build** | The build itself is parallel: N producer goroutines scan+filter claiming blocks from a shared allocator, one consumer owns the map and inserts. | `parallelBuildLazyHashTable`, gated by `parallelBuildEligible` |

**But "goopg already has PG's Parallel Hash" would overstate it, and an earlier
draft of this doc did.** P8 is the analogue of PG's *non-parallel-aware* `Hash`
under a parallel plan, with the per-worker duplication removed — the file's own
header says "P8 keeps the build serial in the leader". PG's `Parallel Hash`
exists largely to handle **spilling** cooperatively (`PHJ_BATCH_ELECT/ALLOCATE/
LOAD/PROBE`, `PHJ_GROW_BATCHES_*` in `hashjoin.h`, plus combined `hash_mem`).
goopg does the opposite: `prebuildSharedHashJoins` **declines to share** any
build projected to need more than one batch, so for exactly the workloads PG
built `Parallel Hash` for, goopg falls back to every worker building its own
private table — PG's worse option. "goopg needs neither" holds only for builds
that fit in `work_mem`.

## 2. Coverage census

`parallelBuildEligible` instrumented; TPC-H Q3, Q10, Q14, Q18 run:

| | count |
|---|---:|
| hash joins whose eligibility was evaluated | **23** |
| got the cooperative parallel build | **8** |
| declined: build side not a scannable subtree (rule 3) | **11** — 9 `*optimizer.Join`, 1 `Project→Filter`, 1 other |
| declined: relation below `MinParallelTableScanBlocks` (rule 4) | **3** (1 block ×2, 228 blocks ×1, vs min 1024) |
| declined: composite key | **1** |

**Two caveats an earlier draft got wrong.**

*It is not a partition of a single population.* `parallelBuildEligible` is called
from `buildLazyHashTable` for **every** lazy hash join in every plan — including
joins in serial plans and joins above the Gather — so "23 evaluated" is not "23
candidates for sharing". Rules 1 and 2 recorded zero declines, which may mean
they were not reached rather than not triggered; the census does not distinguish.

*The declines do not "lose only the parallel build".* An earlier draft claimed
every one of the 23 still got the shared table. That is false, and the census
refutes it: `collectShareableJoins` descends the **probe side only**, so a join
sitting on another join's build side is never collected. The 9 `*optimizer.Join`
build sides are exactly such nestings — those joins get **neither** the parallel
build nor the sharing. Sharing is further withheld from spilling builds, from
`Lateral`, and from any plan with no Gather at all.

## 3. The defect that was real: two walkers, one too narrow

| | descends |
|---|---|
| `attachParallelScan` (operator, `parallel_scan.go`) | `seqScanOp`, `filterOp`, `projectOp`, `instrumentedOp`, `aggregateOp`, `sortOp`, and a `joinOp`'s **probe** side |
| `extractSeqScanFromPlan` (plan, `parallel_hash_build.go`) | `SeqScan`, and `Filter` with a `SeqScan` **directly** beneath |

The plan side could not see `Project → SeqScan` or a chained `Filter`, both of
which the operator side wires without difficulty. A shape rejected there
silently loses the parallel build — no error, no log.

**The fix** widens the plan walker to `Filter` and `Project` chains of any
depth. `TestBuildWalkerAcceptsOnlyWhatCanBeWired` builds each accepted shape
with `BuildWorker` and asserts `attachParallelScan` really can wire it.

**Measured effect: neutral.** Alternating A/B, fresh server per arm, warm
(second of two runs), milliseconds:

| round | arm | Q3 | Q10 | Q14 |
|---|---|---:|---:|---:|
| 1 | baseline | 3276.0 | 2211.8 | 569.2 |
| 1 | widened | 3331.4 | 2256.1 | 572.2 |
| 2 | baseline | 3375.9 | 2219.2 | 568.9 |
| 2 | widened | 3314.3 | 2226.9 | 559.4 |

Within noise in both directions. Only 1 of the 23 joins is a `Project`/`Filter`
chain, and the census does not record whether it also clears rule 4 — so the
honest statement is that **this ships as a consistency fix with a differential
test, not as a speedup**, and no post-fix census was taken to prove coverage
moved at all.

## 4. The extension that looks obvious and is a correctness hazard

`attachParallelScan` also descends a join's probe side, so making the plan
walker match is a small change. **It was implemented, measured, and rejected —
and the first reason is correctness, not cost.**

### 4.1 Correctness

Descending a join does not stop at the join. The walk continues, and
`attachParallelScan` has an `aggregateOp` arm whose own comment states the
safety condition: it is sound because a **Partial** aggregate sits inside the
partial subtree with a **Finalize** above it, which the planner creates for a
Gather.

**The cooperative build creates no such split.** Each producer calls
`BuildWorker(buildPlan)` and gets the *whole* aggregate; `attachParallelScan`
then partitions the scan beneath it. N producers each aggregate their own
partition, and the consumer unions those partial results into the hash table
with no Finalize. For a `HAVING sum(...) > k` predicate — TPC-H Q18's semi-join
build side is precisely that — the answer is **silently wrong rows**.

Today this is unreachable: the plan walker has no `Aggregate` arm, so an
aggregate anywhere in the build chain declines at rule 3. Descending joins
removes that protection, because `Join → probe → Aggregate → SeqScan` becomes
reachable.

So the asymmetry between the two walkers is a **safety property**, not a defect,
and the "make them agree" principle in §3 applies *only* to node kinds that are
1:1 and order-independent — Filter and Project. `extractSeqScanFromPlan`'s doc
comment now says this explicitly, so the next reader does not "fix" it.

### 4.2 Cost, measured

Independently of correctness, each producer redoes every nested build.
Alternating A/B, fresh server per arm:

| query | baseline | with join descent | |
|---|---:|---:|---|
| Q18 round 1 | 35,741.6 ms | 44,059.3 ms | 1.23× slower |
| Q18 round 2 | 35,651.7 ms | 42,940.6 ms | 1.20× slower |

Results were byte-identical on Q3/Q10/Q14/Q18 for the shapes reached in this
experiment, so this measurement is a cost result — it does **not** exonerate
§4.1, whose hazard needs an aggregate to be reached.

### 4.3 A guard that failed, and the wrong reason I gave for it

A first guard used `optimizer.EstimateRows` on the nested build and admitted
everything. An earlier draft explained that as "S-cold means `reltuples = 0`, so
everything estimates tiny". **That explanation is wrong.** `EstimateRows` →
`seqScanRows` falls back to `EstRelRows`, stamped from the
`GOOPG_RELSIZE_FALLBACK` block-count path, which defaults to stage 2 (on). So a
`lineitem` leaf estimates in the millions, not tiny. (goopg also does not read
`pg_class.reltuples` here at all — `tableRows` reads the in-memory catalog's
per-session ANALYZE state.)

Replacing it with measured block counts behaved the same way — which now has a
simpler explanation than the one an earlier draft reached for: **both guards
were fed the same number**, because `EstimateRows` already consumes the block
count. What actually defeated the guard was not diagnosed, and with §4.1 making
the whole direction unsafe, it was not pursued.

## 5. Changes and verification

`internal/executor/parallel_hash_build.go` — walker widened to `Filter`/`Project`
chains; the stale "unwrapping a single Filter" comment replaced with the safety
rationale above. `internal/executor/parallel_hash_walker_test.go` — new.

| check | result |
|---|---|
| Q3 / Q10 / Q14 / Q18 results vs baseline | byte-identical (`md5`: q03 `a522286a65e5`, q10 `6bc520ff7fc0`, q14 `66e84e752b56`, q18 `038471180a04`; rows 11415 / 20451 / 1 / 12) |
| A/B Q3 / Q10 / Q14 | §3 table — within noise |
| `TestBuildWalkerAcceptsOnlyWhatCanBeWired` | pass (6 wirable shapes checked against `attachParallelScan`, 3 refused) |
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | pass — exit 0, 43 packages, 0 failures |
| `go test -race ./internal/executor/` | pass |
| `scripts/tpch-spotcheck.sh` | **PASS** — exit 0, Q12 = 2, Q13 = 34 |

## 6. Known gaps this did not touch

1. **Join descent** — blocked on §4.1 first, then on a cost model.
2. **Cooperative build escapes both parallelism GUCs.** `parallelBuildLazyHashTable`
   sizes from cluster-wide `MaxParallelWorkers` and **floors at 2**, so
   `max_parallel_workers_per_gather = 0` does not disable it and even
   `max_parallel_workers = 0` still spawns 2 producers. Pre-existing.
3. **It also skips the Gather path's parallel-safety gate** — no DML/DDL,
   `SERIALIZABLE`, `LockRows` or temp-relation checks; only `preserveCTIDRel`.
   A ≥1024-block temp table on a build side under `SERIALIZABLE` goes N-way
   parallel ungated. Pre-existing.
4. **Cooperative spilling** — the thing PG's `Parallel Hash` is actually for
   (§1). goopg declines to share a spilling build entirely.
5. **Composite-key builds** (1 decline) and **rule 4's threshold**, borrowed
   from scan sizing and untested as a *build* bound (3 declines).

---

## 7. Review record

Adversarial agent review, 2026-08-30, against the first draft: **3 critical,
9 major, 5 minor**. All corrected above. One finding changed the *shipped code*,
not just the prose.

| # | finding | resolution |
|---|---|---|
| **C2** | "descending a join is correct but slow" — **false**. The walk does not stop at the join; it reaches `attachParallelScan`'s `aggregateOp` arm, whose safety rests on a Partial/Finalize split the cooperative build never constructs. N producers would each aggregate their own partition and the consumer would union them with no Finalize: for `HAVING sum(...)` — Q18's build side — **silently wrong rows**. | §4.1 rewritten: correctness is the first reason to decline, cost the second. **The shipped code comment was rewritten too** — it previously said the two walkers "MUST agree", which would invite a future reader to add the very arm that breaks it. It now states the asymmetry is a safety property and why. |
| **M1** | The doc's "make the two walkers agree" principle is unsafe as stated, and its list of `attachParallelScan`'s arms omitted `aggregateOp` and `sortOp` — the two that make it unsafe. | Arms listed in full; the principle narrowed to 1:1, order-independent nodes. |
| **C1** | "Every one of the 23 still gets the shared table" — **false**. `collectShareableJoins` descends the probe side only, so the 9 join-build-side cases get neither the parallel build nor the sharing; sharing is also withheld from spilling builds, `Lateral`, and plans with no Gather. | §2 corrected, including that "23 evaluated" is not a population of sharing candidates (`parallelBuildEligible` runs for every lazy hash join in every plan, serial ones included). |
| **C3** | The S-cold/`EstimateRows` diagnosis was wrong: `EstimateRows` falls back to `EstRelRows` via `GOOPG_RELSIZE_FALLBACK` (default on), so a `lineitem` leaf estimates in the millions. goopg does not read `pg_class.reltuples` here at all. | §4.3 corrected, with the simpler real explanation — both guards consumed the same block count — and an admission that the true cause was never diagnosed. |
| **M2** | The test never called `attachParallelScan`; it was a hardcoded table that would stay green if the `projectOp` arm were deleted — exactly the drift it was named for. | Rewritten as a genuine differential test: every accepted shape is built with `BuildWorker` and must be wirable by `attachParallelScan`. |
| **M4/M5** | §3 and §5 cited each other for A/B data that appeared in neither; the gate row pointed at a section containing no gates. | Real numbers and `md5`s inlined; gate results stated. |
| **M6** | "goopg already has [PG's Parallel Hash]" overstates P8, which is the non-parallel-aware `Hash` with duplication removed — PG's `Parallel Hash` exists largely for cooperative *spilling*, which goopg declines outright. | §1 corrected; the spilling gap added to §6. |
| **M3** | The Q18 causal story was self-contradictory: if the semi-join build side is a `HashAggregate`, rule 3 declines it before and after, so it cannot be the node the experiment changed. | §4.2 no longer attributes the regression to a specific node; the measurement stands as a cost result only. |
| **M7** | The cooperative build escapes both parallelism GUCs (floors at 2 producers) and the Gather path's parallel-safety gate. | Added to §6 as pre-existing gaps. |
| **M8/M9** | Rules 1 and 2 show zero declines, which may mean not-reached rather than not-triggered; and no post-fix census proves coverage moved. | Both stated as caveats rather than glossed. |
| m1–m5 | `gatherMergeOp` misnamed; a stale "unwrapping a single Filter" comment left stacked above the new one; code comment quoted 43.5 s as a single figure rather than the two rounds; producer-count convention ambiguous; "three-line change" understated (it needs a fourth copy of `probeSideIsLeft`). | Fixed; the stale comment removed. |

Verified **correct** by the review: that `prebuildSharedHashJoins` is reached
from both Gather open paths before any worker starts; that the table is shared
by pointer end-to-end; that `parallelBuildLazyHashTable` is genuinely N
producers + 1 consumer and that each producer redoes the whole build subtree;
that §1's description of PG's two options matches `hashjoin.h`; that the census
arithmetic is consistent; that rule 4's threshold is `MinParallelTableScanBlocks`;
and — the one that matters most for the shipped change — that **the new walker
cannot infinite-loop and accepts no shape `attachParallelScan` would reject**,
so the dangerous direction does not exist here.
