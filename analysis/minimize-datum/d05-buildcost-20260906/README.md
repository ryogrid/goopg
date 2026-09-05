# Build-cost under-pricing — implemented, measured, **REVERTED**

Date: 2026-09-06. Fifth measurement in the chain D-04 → entry-width →
bucket-charge → cost-side narrowing → **this**. Patch preserved at
`tmp/d05p4-buildcost-mine.patch`.

Like its four predecessors it disproves the previous round's prediction. The
cost-side write-up named `hashJoinCost`'s under-priced build as "the next
blocker, new and precisely located". It is real, it is confirmed here with a
live witness, and **fixing it does not unblock anything**: charging it costs
TPC-H **+22.3%**, and re-applying the two preserved patches on top of the fix
costs **+21.1%** — the same three queries, the same amount, by a mechanism
neither the ledger row nor any earlier round had identified.

## 1. The two unmodelled terms, confirmed

### 1a. The five private per-worker builds — CONFIRMED, live

`prebuildSharedHashJoins` (`internal/executor/parallel_hash_build.go:191`)
declines the **share**, not the spill:

```go
if sharedBuildWouldSpill(ctx, j) {
    continue
}
```

and `sharedBuildWouldSpill` (:220) is exactly
`j.buildGeometry(...).NBatch > 1`. A second, measured check follows the build
(:198) and throws the leader's table away if the estimate was wrong.

`EXPLAIN (ANALYZE)` of TPC-H Q9 on the SF1 bench cluster prints the
consequence directly (full plan:
`q9-witness-explain-analyze.txt`):

```
Gather   Workers Planned: 4   Workers Launched: 4          -> 5 participants
  Hash Join (orders)  Buckets: 1048576  Batches: 4
                      Memory Usage: 93178kB
                      Build Time: 2979.329 ms   of 8062.371 ms Execution Time
    -> Seq Scan on orders
         Worker 0: rows=1500000.00 loops=1
         Worker 1: rows=1500000.00 loops=1     <- FIVE full private scans
         Worker 2: rows=1500000.00 loops=1
         Worker 3: rows=1500000.00 loops=1
         Worker 4: rows=1500000.00 loops=1
```

while the **shared** builds in the same plan print the opposite signature:
`partsupp` (`Build Time: 1561.753 ms`, no `Batches:` line — composite key) and
`part` (`Batches: 1`) show `rows=0.00 loops=0` in every worker, because the
leader built them once and published them. The replication is therefore not a
modelling assumption; it is printed by the plan, and it is gated on precisely
`NBatch > 1`.

**37% of Q9's elapsed time is a build the cost model charges once at
`cpu_tuple_cost`.**

### 1b. The bucket array outside the spill branch — CONFIRMED, and inert

`sizing` was used in exactly one place in `hashJoinCost` — the
`if sizing.NBatch > 1` branch. The bucket array entered the COST only through
the batch decision it helped trigger; a resident build paid nothing for it.
On the Q9 witness that is 1,048,576 slots × 48 B = **48 MB, allocated five
times** (D-04 measured 506 MB of live Go map at this site).

Where the multiplier belongs: inside the build term
(`cost_funcs.go:hashJoinCost`), which is `inner.Total` plus
`(cpu_operator_cost·k + cpu_tuple_cost)·innerRows`, charged as startup.

## 2. What PG does that goopg does not

| | PG 18.3 | goopg |
|---|---|---|
| **execution** — parallel build | ONE shared table: `ExecParallelHashTableCreate` (`nodeHash.c`), all participants insert into it | no shared parallel hash. The leader pre-builds and publishes, or — if the build would spill — **every participant builds privately** (`prebuildSharedHashJoins`) |
| **execution** — budget | `ExecChooseHashTableSize`'s `try_combined_hash_mem` gives a parallel hash `(workers+1) × hash_mem`, and falls back to per-worker only if that still batches | `hashsize.Choose` has no combined-budget path at all (documented absent, 06 §6): one `work_mem × hash_mem_multiplier` per private build |
| **execution** — bucket array | `bucket_bytes = sizeof(HashJoinTuple) * nbuckets`, 8 B/slot, folded into `spaceUsed`, with `Assert(bucket_bytes <= hash_table_bytes/2)` | 48 B/slot (`MapSlotBytes`, measured 2× low — see the `mapslotbytes` round), pre-deducted from `spaceAllowed`, **no assert** |
| **cost** — build charge | `initial_cost_hashjoin`: `startup += inner_path->total_cost + (cpu_operator_cost*k + cpu_tuple_cost) * inner_path_rows`, charged ONCE — **correct there, because there IS only one build** | the same formula, charged once — **incorrect here, because there are up to five** |
| **cost** — parallelism | the whole model is parallel-aware: partial paths carry divided rows/costs, `get_parallel_divisor` divides the outer, `create_gather_path` prices the funnel, and `parallel_hash` is an input to the sizing | **the cost model has no parallel dimension.** `MaybeAddGather` (`parallel.go:100`) is a POST-PLANNING size rule over the finished `Node` tree; `PartialPathlist` is populated by `generateScanPaths`, which production never calls |
| **cost** — path width | `page_size(rows, path->pathtarget->width)` on BOTH sides, and a pathtarget is already narrowed | `pathNCols`/`pathAvgVarBytes` fall back to the whole rel on both sides (the `costside-unnarrowed` item), and the preserved patch narrows the INNER only |

The last cost row is the one that decided this experiment. See §6.

## 3. The change, and its direction of error

`internal/optimizer/cost_funcs.go`:

```go
sizing := hashsize.Choose(in.innerRows, in.innerCols, in.innerAvgVarBytes, cp.workMem)

build := (cp.cpuOperatorCost*float64(in.numHashClauses)+cp.cpuTupleCost)*in.innerRows +
    in.inner.Total + hashBucketArrayCost(cp, sizing)
build *= hashBuildReplication(sizing)     // 5 when sizing.NBatch > 1, else 1
```

- `hashBuildReplication` returns the modelled participant count
  (`max_parallel_workers_per_gather`'s boot default 4, plus the leader) for a
  build the executor would decline to share, and **1 otherwise**. Overridable
  with `GOOPG_HASH_BUILD_REPL` (registered in the flag-provenance table).
- `hashBucketArrayCost` charges `cpu_operator_cost` per bucket slot **above
  `MinBuckets`**. The 1024-slot floor is subtracted deliberately: every hash
  build allocates it, so charging for it adds the same constant to every hash
  path — no discrimination between plans, only a shove at the
  hash-vs-nestloop/mergejoin crossover, which is the direction that cost Q14
  +3364% in the `mapslotbytes` round.

**Why the multiplier goes on the build and not the probe.** The model is
serial: it charges the probe its full, undivided cost. Elapsed time is
`build + probe/D`. Ranking by `D·build + probe` is order-equivalent to ranking
by elapsed; ranking by `build + probe` is not. So the correction is not "pay
for the extra copies" — it is "stop pricing an unparallelised phase as if it
were parallelised alongside the phase that is".

**Calibrated on the witness, not guessed.** Q9's build is 2979 ms of 8062 ms;
in the model's serial units the build:probe ratio is 0.85. The model read
`213116 : 862520 = 0.25`, under-weighting the build by ≈3.5×. Multiplying only
the child+CPU term (116023) by 5 gives 0.79. Multiplying the spill term too
gives 1.24, an over-shoot — which is why the spill charge is left at 1×.

**Direction of error: HIGH on a spilling build, exactly 1× on a resident one.**
The gate is the executor's own rule, so every `Batches: 1` build — Q14's `part`
included — is priced exactly as before and the Q14 flip is unreachable by
construction. It is still an over-charge for any plan that never gets a Gather,
because the worker count does not exist at costing time and cannot be made to:
`ParallelSettings` is deliberately outside the plan-cache key
(`dispatch.go:1967`), so a settings-dependent cost would make cached plans
session-dependent.

## 4. Round 0 — plan census (EXPLAIN only, 22 queries, one server per arm)

| arm | plans differing from base | Gather lost |
|---|---|---|
| `REPL=1` (bucket-array term alone) | **none** | — |
| `REPL=2` | Q7, Q9 | Q9 |
| `REPL=3` | Q7, Q9 | Q9 |
| `REPL=5` (the calibrated default) | Q5, Q9, Q10, Q21 | Q5, Q9, Q10 |

**The bucket-array term is inert on this suite** — zero plans move at
`REPL=1`. That is a clean negative and it is worth recording: the term the
ledger row asks for as half of the fix cannot decide a TPC-H plan at any
honest magnitude. (Its first cut, charged at `seq_page_cost` per BLCKSZ,
*could*: it put a flat 6.0 surcharge on every hash join including a 100-row
one, and three unit tests caught it immediately.)

Everything below is at `REPL=5`.

## 5. Round 1 — the fix alone: **+22.3%**

22 queries, 2 reps per arm, interleaved, one binary per arm, fresh capped
server per arm, statistics pinned (`GOOPG_ANALYZE_SEED=20260905`),
`NO_BUILD=1 PGSHAPED=1 COLLAPSE=1 PER_Q=600`.

| | rep 1 | rep 2 | best |
|---|---|---|---|
| base | 122.72 | 122.71 | **121.98** |
| fix | 151.14 | 149.69 | **149.15 (+22.3%)** |

The base arm's A/A spread is **0.01%**, so this is not close to noise.

Every query that moved:

| query | base | fix | Δ | what changed |
|---|---|---|---|---|
| Q5 | 3.65 | **19.84** | **+444%** | Gather + hash cascade → **serial**, Merge Join on `orders_pk`×`idx_lineitem_orderkey_fkidx` |
| Q10 | 2.47 | **7.93** | **+221%** | Gather + hash → **serial**, Merge Join on `orders_pk` × seq `lineitem` |
| Q9 | 6.49 | **13.94** | **+115%** | Gather + 4-deep hash cascade → **serial**, two nested Merge Joins |
| Q21 | 12.49 | 12.34 | −1.2% | one Hash Join → Merge Join, timing-neutral |

No other query moved outside ±7% and nothing else changed shape.

- **Values: PASS.** `24 MATCH` on all five pairings (A1/B1, A2/B2, A1/A2,
  B1/B2, A1/B2).
- **Plan parity** (`scripts/pg-plan-parity-diff.py` against
  `bench/tpch/plans-pg`): verdict counts identical
  (`match=1 shapediff=14 missingnode=6 error=2`). Categories moved
  `join-method 11→12`, `scan-type 10→11`, `qual-placement 4→6`,
  **`parallelism 11→8`** — and that last one is not an improvement: goopg's
  divergence in the parallelism category fell because goopg **stopped being
  parallel**.
- **TPC-DS SF0.5**: see §8.

## 6. Why it loses — the finding

The three losing queries do not lose a build-side choice. **They lose
parallelism**, and the cost model cannot see it.

`drivingScan` (`internal/optimizer/parallel.go`) — the function that decides
whether a subtree can be partial — returns `nil` for a plain `*IndexScan`, and
descends a `*Join` only through `hashJoinIsPartialCapable`, which requires
`p.Algo == JoinAlgoHash`. So **a Merge Join anywhere on the driving path makes
the entire plan serial**, and `MaybeAddGather` runs *after* the search, so the
search prices the winning plan as though the 5-way parallel probe it just
threw away had never existed.

That is the actual shape of the trap this whole chain has been walking into:

- the `mapslotbytes` round raised a build's price by 2 MB → Q14 flipped to a
  nested loop, +3364%;
- the `costside` round raised the *small-build* orientation's price (it
  narrows only the inner, so the small-build orientation still paid
  `2 × outerPages` at `lineitem`'s full width) → Q5/Q7/Q9/Q10 flipped, +10.3%;
- this round raised a spilling build's price honestly → Q5/Q9/Q10 flipped to
  serial merge joins, +22.3%.

Three different, individually correct corrections; one failure mode. **Every
one of them was a transfer of work away from a hash join, and in goopg a hash
join is the only join a Gather can sit on.** The model has no term for that.
Until it does, the accuracy of `hashJoinCost` is not the binding constraint —
its *sign* relative to `mergeJoinCost` is, and that comparison is being made
in the wrong units.

## 7. Round 2 — the combination: the coupling hypothesis is **REFUTED**

The chain's central hypothesis was that the two preserved patches are correct
but blocked behind this fix. Measured (2 reps, same protocol; arm C =
fix + `d05p3-costside-narrow` + `d05p2-bucket-charge`):

| | base | fix alone | fix + both patches |
|---|---|---|---|
| TOTAL (best of 2) | **121.98** | 149.15 (+22.3%) | **147.66 (+21.1%)** |
| Q5 | 3.65 | 19.84 | 19.27 |
| Q9 | 6.49 | 13.94 | 13.89 |
| Q10 | 2.47 | 7.93 | 6.50 |
| Q18 | 31.25 | 30.15 | 29.64 |
| Q14 | 0.43 | 0.43 | **0.41 — still no flip** |

Values `24 MATCH` on A1/C1, A2/C2, C1/C2 and B1/C1.

Two things follow.

1. **The earlier coupling finding survives.** Q14 does not flip in the
   combination (0.41 s against 14.55 s when the bucket charge landed alone).
   The phantom 9-column build really was what pushed it over the budget.
2. **The blocking claim does not.** With the build charged honestly, the
   narrowing is *still* a 21% loss, and it is the same three queries losing
   the same way. The build under-pricing was not what stood in the narrowing's
   way. Parallelism was, in both.

For completeness the census also reproduces the previous round exactly: at
`REPL=1` the combination moves Q5, Q7, Q9, Q10, Q12, Q18 — the six queries the
`costside` write-up reported, no more and no fewer.

## 8. Gates

- `go test ./internal/optimizer/ ./internal/executor/` — **green**, including
  `TestSlice3LiveQ9ShapeDerivation`, the named build-side-flip guard. It does
  NOT fire on this change: the change moves *join method*, not build side.
- Three unit tests caught the bucket term's first mis-calibration
  (`TestHashJoinCost_SpillTermFiresExactlyWhenTheExecutorSpills`,
  `TestSlice2WitnessModelArithmetic`, and the three
  `Test*PropagationKeepsDefaultPlan` cases, which read a 6.00 surcharge on a
  100-row join). All three were updated to hand-compute the new terms rather
  than to restate them.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — **green**.
- **TPC-DS SF0.5** (`scripts/tpcds-sf05-regression.sh sweep` at `REPL=5`,
  log alongside this file):
  `PASS=95 (57 ck-verified, 38 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0
  TIMEOUT=0 SKIP=4`, and the status-delta channel reports
  `compared=99 verdict-changes=none runtime-moves=0 total-delta=+1.5%`.
  **The change is invisible on TPC-DS** — which is itself informative: the
  TPC-H losses are specific to plans that were carrying a Gather, and TPC-DS
  SF0.5's queries are small enough that the size ladder grants few.

## 9. Verdict: REVERT

The change is correct in the sense the ledger row asked for — it charges a
build the executor really performs five times, it is calibrated against that
build's measured share of elapsed time, and it leaves resident builds
untouched by construction. It costs 22%.

Reverted. The two preserved patches stay preserved and stay unapplied.

## 10. The next blocker, re-located

**`hashJoinCost` is not the problem. The absence of parallelism from the cost
model is.** Concretely, in priority order:

1. goopg costs a serial plan and then decides parallelism with a post-hoc size
   rule, so the search cannot prefer a plan *because* it will parallelise. Any
   term that makes a hash join dearer therefore trades a real 5× speedup for a
   modelled saving, and TPC-H has now paid that toll three times.
2. The narrowing asymmetry is a second, independent defect and is cheap:
   `d05p3-costside-narrow` narrows the INNER only, while PG's
   `initial_cost_hashjoin` reads `pathtarget->width` for **both** sides. The
   small-build orientation is consequently over-charged
   `2 × page_size(outerRows, un-narrowed width)`. Fixing that alone might make
   the narrowing patch land, and it is a much smaller change than (1).
3. The bucket-array term is inert on TPC-H and can be dropped from the D-05
   prerequisite list, or kept as bookkeeping. It decides nothing.

## 11. Not measured / caveats

- **A peer agent edited `internal/optimizer/cardinality.go` mid-session**
  (03:37:08, grouping-sets estimation). It is in NEITHER measurement binary —
  both were built before that timestamp — and TPC-H has no `GROUPING SETS`, so
  the arms are unaffected. Recorded because the tree was not clean.
- **The bench cluster's WAL was damaged mid-session** and every server start
  after 03:38 needed `GOOPG_WAL_ALLOW_EARLY_END=1`. Table row counts were
  verified intact (`lineitem` 6001255, `orders` 1500000, …) and the flag was
  set identically on every arm, so it cannot bias an A/B. The trigger appears
  to be a server that died during a Q7→Q9 pair; the durable-end pointer
  (3888696017) never regressed afterwards, so every subsequent replay believed
  in WAL that was not there. That is a separate goopg durability-bookkeeping
  bug and is not investigated here.
- No heap sampling this round. The memory effect of a 5× build charge is
  irrelevant while the change is reverted.
- The participant count is a modelled constant (5), not a per-plan quantity;
  a plan that would never be gathered is over-charged by that whole factor.
  Making it per-plan needs a cost-time worker count, which needs
  `ParallelSettings` in the plan-cache key — i.e. item (1) of §10.
