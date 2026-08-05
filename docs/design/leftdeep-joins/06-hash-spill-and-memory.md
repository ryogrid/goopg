# 06 — Hybrid Hash Join: work_mem-Bounded Builds and Batch Spill

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| PG oracle | `postgres/src/backend/executor/nodeHash.c` (`ExecChooseHashTableSize` :658, `ExecHashTableCreate` :446, `ExecHashIncreaseNumBatches` :1030) ; `postgres/src/backend/executor/nodeHashjoin.c` (`ExecHashJoinSaveTuple` :1414, `ExecHashJoinGetSavedTuple` :1455, `ExecHashJoinNewBatch` :1130 incl. the batch-skip rules :1172-1202 and the reload-growth note :1236-1242, outer-tuple batching in `ExecHashJoinOuterGetTuple` :979) |
| depends on | [05](05-executor-pipeline-rework.md) stage E3 (single-pass build) |
| reuses | `spillWriter`/`spillReader` (`internal/executor/spill.go:20-97`) as the batch-file primitive; `sortOp`'s multi-file lifecycle (`operators.go:590-913`) as the management template |

## 1. The gap

No goopg hash table spills. `drainRowsBounded` (`spill.go:342`) bounds only
the build-side *staging list*; every row still lands in the in-memory map
afterwards, so `ctx.WorkMem` (default 512 MiB) is not actually a bound on
join memory. Measured consequences: Q21 OOM at SF1 without MHJ
(`stage2-fusion-verdict.txt` Arm A: OOM at 244 s, then server dead on Q22);
the "HANG / cancellation not honoured" class under `GOMEMLIMIT` with
`GOGC=off`; and a planner that cannot honestly price big builds
([04](04-cost-and-cardinality.md) §4). `multiHashJoinOp` is worse — its
drain is unbounded `drainRowsCtx` (`multi_hash_join.go:184`) with no
`WorkMem` reference at all — which is one of the five reasons it dies rather
than graduates.

## 2. Design: PG's hybrid hash, goopg-sized

Implement the classic multi-batch scheme inside the (post-E3) single-pass
build of `joinOp`:

### 2.1 Sizing — `chooseHashTableSize(innerRows, innerWidth, workMem)`

Direct analogue of `ExecChooseHashTableSize` (nodeHash.c:658): returns
`(nbuckets, nbatch)`, both powers of two; `nbatch = 1` when the projected
table fits `workMem`, else the smallest power of two making each batch fit.
Width uses goopg's real entry cost — `estimatedRowBytes` semantics
(`spill.go:324`: `48·len(row)` + payload) **plus** map-entry overhead — not
PG's MinimalTuple math. This function is **shared with the planner**
([04](04-cost-and-cardinality.md) §4) so cost and execution cannot diverge
(sibling-path rule).

### 2.2 Partitioning

`batchno = (hash >> bucketBits) & (nbatch-1)` — hash bits disjoint from
bucket selection, PG's scheme. During build (single pass, E3): batch 0 rows
go to the in-memory table; rows hashing to batch > 0 are written to that
batch's `spillWriter` file (`ExecHashJoinSaveTuple` analogue — the existing
uvarint row framing in `spillWriter.WriteRow` gains a 4-byte hashvalue
prefix so re-load never re-evaluates key exprs, matching PG's saved-tuple
format rationale).

### 2.3 Probe

While processing batch 0, probe rows whose hash lands in batch > 0 are saved
to per-batch **outer** files instead of probing
(`ExecHashJoinOuterGetTuple` behaviour). After batch 0 drains:
`newBatch(k)` loads inner batch k's file into the (cleared) table, then
replays outer batch k's file as the probe stream
(`ExecHashJoinNewBatch`). `nextLazy`'s state machine grows exactly one state
("load next batch"), mirroring PG's `HJ_NEED_NEW_BATCH`.

Two PG rules that are easy to miss and are binding here:

- **Replayed tuples re-check their batch number.** If nbatch grew after a
  tuple was saved (§2.4), a reloaded inner tuple or a replayed outer tuple
  may now belong to a *later* batch — it is re-routed forward (re-spilled),
  not processed in place (PG's rules 2 and 3, `nodeHashjoin.c:1172-1202`;
  reload goes through the same batching insert path with the batchno
  recomputed from the saved hashvalue).
- **Fill-aware batch skipping.** A batch may be skippable when one side is
  empty — but only respecting fill obligations (PG rule 1): with LEFT/FULL
  fill, outer-only batches must still be processed (their probe rows emit
  null-padded); with RIGHT/FULL fill, inner-only batches must still be
  scanned for the unmatched sweep. Skipping an outer-only batch under a
  LEFT join silently loses fill rows — a correctness bug the SF0.5 gate
  would catch only late.

### 2.4 Growth

If the in-memory table overflows during **build**, double `nbatch` and
evict rows now belonging to later batches to their files
(`ExecHashIncreaseNumBatches`, nodeHash.c:1030). Growth can also be
triggered while **reloading a later batch** (`nodeHashjoin.c:1236-1242` —
the estimate-was-low case that caused mid-build growth is exactly the case
where a skewed later batch overflows again at reload); the mechanism is the
same because reload runs through the batching insert path. When doubling
stops helping (all rows hash to one batch), PG freezes growth
(`growEnabled = false`, `nodeHash.c:1182-1184`): nbatch stays fixed and the
**current batch alone** overruns memory; already-spilled batches stay
spilled. goopg mirrors that freeze semantics with a WARNING-level log, so
pathological skew degrades rather than thrashes.

### 2.5 Semi/Anti/outer interaction

- Semi/Anti: identical batching; the per-probe-row "emit at most once"
  logic is batch-local because a probe row belongs to exactly one batch.
- NULL-aware anti (NOT IN): `antiBuildHasNull` must be computed across ALL
  batches before any early-out — the build pass already sees every row, so
  the flag is global by construction; the row-list short-circuit
  (`antiBuildRows`) works per-batch.
- LEFT fill stays **inline on probe miss** (as today), per batch — each
  probe row meets its full match set within its own batch. The
  **post-replay sweep** is RIGHT/FULL's build-side fill only
  ([07](07-other-join-operators.md) §3), run per batch after that batch's
  probe replay completes, subject to the fill-aware skip rules of §2.3.

## 3. Temp-file hygiene (new obligations)

The current primitives leak: `spillOp.Close` does not remove its file
(`spill.go:470-473`); files go to bare `os.TempDir()`. This chapter adds the
join's obligations and fixes the shared substrate:

- per-query spill registry on `Context`: every temp file created by any
  operator registers for close+unlink at query end (normal or error path) —
  the `sortOp` remove-on-Close discipline generalised;
- files under `<datadir>/base/pgsql_tmp/` per PG convention (crash sweep at
  startup deletes strays), replacing `os.TempDir()`;
- `WaitBuffileWrite`/`WaitBuffileRead` activity accounting stays (the cached
  registry pattern from `spill.go:36-45` — the `runtime.Stack` regression
  class must not return).

## 4. Observability

- `EXPLAIN (ANALYZE)` hash-join line reports `Batches: N (originally M)` and
  peak in-memory bytes, PG format, sourced from the operator's counters;
- the existing instrumentation wrapper (`maybeInstrument`) needs no new
  hooks — counters live on `joinOp`.

## 5. Planner contract

**Status: LANDED (M0127-P5.7-a, 2026-08-05)**, with one clause of it deferred.

`hashJoinCost` calls the same `chooseHashTableSize`; `nbatch > 1` adds
`(inner_bytes + outer_bytes_spilled_fraction) × 2 / BLCKSZ × seq_page_cost`
I/O (write + read), per `final_cost_hashjoin` (costsize.c:4275). The planner
also exposes `nbatch` on the HashJoin path for EXPLAIN parity of estimates.
This term is what replaces the deleted quadratic penalty as the honest
deterrent against building on huge intermediates
([04](04-cost-and-cardinality.md) §1, §4).

As built: the shared function is `hashsize.Choose` (this chapter's
`chooseHashTableSize`), and the term applied is upstream's exact split rather
than this section's prose sketch — `seq_page_cost · innerPages` at STARTUP
plus `seq_page_cost · (innerPages + 2·outerPages)` at run
(costsize.c:4239-4248), which totals the same `2·(inner+outer)` pages while
putting the inner write where it actually happens. There is no
"spilled fraction": PG charges the whole outer, because with `nbatch > 1`
every outer row is routed through a batch file.

**Not done — `nbatch` is not exposed on the HashJoin path.** The geometry is
recomputed inside `hashJoinCost` and discarded; nothing carries it to
`createPlan` or to EXPLAIN, so `EXPLAIN` cannot yet show the planner's batch
prediction beside the executor's actual `Batches:` line. That comparison is the
natural instrument for the width approximation named in
[04](04-cost-and-cardinality.md) §4.1, and it is ledgered (2026-08-05
M0127-P5.7-a) rather than built, because `Path` is deliberately kept small and
the field earns its place only once something reads it.

The other half of the contract — that a session's `work_mem` reaches the
planner at all — is NOT satisfied: `costParams.workMem` is fixed at
`hashsize.DefaultMemLimitBytes`, the executor's own fallback. Planner and
executor therefore agree exactly at the default and diverge for any session
that sets `work_mem`. Same ledger row.

## 6. Deferred (ledger rows at implementation time)

- **Skew optimisation** (PG's MCV skew buckets, `ExecHashBuildSkewHash`) —
  valuable for Zipfian keys; not needed to clear this bundle's bars.
- **Parallel hash** (shared batches across workers) — parallel-query bundle
  territory; the leader-serial shared build keeps working with nbatch = 1
  builds only (a shared build that would spill declines sharing and each
  worker builds privately — correctness first, noted in
  `parallel_hash_build.go` docs).
- **Hash-agg spill** — same primitives, different operator; explicitly out
  of scope here.
