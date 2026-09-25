# M0146-0002: `Parallel Hash` over a genuinely partial inner

Status: COMPLETE 2026-09-24 — slices 1 (`171c5d58a`) and 2 (`cf02e88b9`) landed, TPC-H Q14 matches PG; slice 3 attributed Q16's residue to the NOT IN route (M0146-0002b). Category regressions: M0146-0002a.
`.ralph/fix_plan.md` M0146-0002 (Kind: impl, Parent: M0140-0007). Scope was
set by the owner's 2026-09-23 answer to M0140-0007, option (a): a fidelity
port. The label and the execution model land together, each shape pinned by a
parallel-vs-serial identity test. Predecessor recon:
`m0140-0007-parallel-hash-shared-build.md`.

## What PG does

`try_partial_hashjoin_path(..., parallel_hash = true)`
(`./postgres/src/backend/optimizer/path/joinpath.c:1290-1297`, dispatched from
`hash_inner_and_outer`, `:2418+`) takes a PARTIAL inner. At execution
(`./postgres/src/backend/executor/nodeHash.c` `MultiExecParallelHash`,
`nodeHashjoin.c` `ExecParallelHashJoin`):

- every participant runs its share of the partial inner (block-claimed
  `Parallel Seq Scan`) and inserts into ONE shared table;
- the build barrier (`PHJ_BUILD_*` phases, `BarrierAttach` /
  `BarrierArriveAndWait`) holds every attached participant until the build is
  complete; only then does any participant probe;
- attachment is dynamic: a participant that arrives after the build phase
  skips building and probes the finished table;
- an error in any participant fails the query (`ERROR` aborts the parallel
  group).

EXPLAIN renders `Parallel Hash Join` over `-> Parallel Hash -> Parallel Seq
Scan`.

## What goopg does today

`parallel_hash = false` only. A Gather prebuilds each shareable hash join's
table in the leader, cooperatively (`parallelBuildLazyHashTable`, a
leader-inserted producer/consumer build over a COMPLETE inner). Every
participant then adopts the frozen table (`prebuildSharedHashJoins`,
`applySharedBuild`). `joinpathsparallel.go` never reads
`inner.PartialPathlist`.

## Design

### Plan

`optimizer.Join.ParallelHash` marks a hash join whose build side is PARTIAL.
It is legal only under a Gather, on the partial path, and only for join types
whose build side needs no after-probe sweep: INNER, SEMI, ANTI, and LEFT with
the build on the right. RIGHT/FULL (PG's parallel-aware unmatched sweep) are
out of scope and ledgered.

### Executor (slice 1)

- **Registration.** `gatherOp.Open` / `gatherMergeOp.Open` walk the partial
  plan the way the claim walks do, and create one `parallelHashBuild` per
  `ParallelHash` join. They publish the map on the session context before
  any worker exists, like `SharedHashBuilds`; worker contexts copy the
  reference. Close retracts it after the fan-out has joined.
- **Claims.** The build side gets its OWN claim state, one per join, grown
  lazily and thread-safely like `setOpBranch`. The probe side keeps the
  Gather's. One state for both would share `parallelScanState`'s
  first-opener block count across two different relations, which is the
  SetOp hazard.
- **Leader prebuild excluded.** `HasShareableHashJoin` and
  `collectShareableJoins` skip a `ParallelHash` join and keep descending its
  probe side, so the leader never builds it. `parallelBuildEligible` refuses
  it: the cooperative builder would rebuild the subtree over its own scan
  state and read the whole relation.
- **Participant protocol** (`openParallelHashJoin`):
  1. `attach()`. If the build is already complete, adopt the table (a late
     participant builds nothing, as with PG's `BarrierAttach` past the build
     phase).
  2. Otherwise run the ordinary build (`buildLazyHashTable`) over this
     participant's claimed share of the inner, into a private table.
  3. `finish(local, err)`: under the lock, merge the private table into the
     shared one and count this participant finished. When `finished ==
     attached` the build is complete and the done channel closes. That is
     sound because a participant only finishes after its inner scan
     returned EOF, i.e. every block of the inner was claimed. Every
     participant still processing a claimed block is attached and not
     finished, so completion cannot fire early.
  4. `wait()` returns when done or when the group context is cancelled. A
     build error recorded by any participant is returned to every waiter:
     the query fails loudly and never probes a partial table.
  5. Adopt the merged table read-only (the `applySharedBuild` field set),
     then open the probe side.
- **Merge semantics.** String and int64 maps merge by appending each key's
  rows. The int64 lane is chosen from the plan's key types, so every
  participant builds the same representation. `antiBuildRows` sums and
  `antiBuildHasNull` ORs (NOT IN's three-valued NULL must see every
  participant's NULLs).
- **Spilling.** A participant build that ends with batch state is refused
  with an error, never silently kept partial. PG's parallel hash batches
  (`ExecParallelHashIncreaseNumBatches`) are ledgered. The planner slice
  files the path only when the inner fits `hash_mem`.

### Planner (slice 2)

`addPartialHashJoinPath`'s `parallel_hash = true` arm reads
`inner.PartialPathlist` and prices the build as PG does: the inner's partial
total cost, with the table shared (`initial_cost_hashjoin` /
`final_cost_hashjoin` with `parallel_hash`, `costsize.c`). EXPLAIN renders
`Parallel Hash Join` / `Parallel Hash`.

### Measurement (slice 3)

The canonical parallel TPC-H capture: Q14 and Q16 are the witnesses (both
engines build over `part`). The expected movement is those two leaving
`parallelism`. Q9/Q21/half of Q10 are join-order divergences
(M0146-0005), not this task.

## Hard constraint

No probe before the shared build completes, and a failed build fails the
query. Pinned by parallel-vs-serial identity tests per shape, not predicate
tests (the 2026-09-21 parallel SEMI lesson).

## Slice 1 landed (2026-09-24, `171c5d58a`)

As designed, plus what the implementation surfaced:

- **"Spilled" is `nbatch > 1`, not a batch state.** P3.2 installs a batch
  state even for a one-batch build, so the memory bound stays real. The first
  cut refused every build as spilled.
- **The leader's wait watches the Gather group.** Workers' contexts derive
  from the group, but the leader waits on its statement context. The barrier
  also selects on the group's `Done`, or a leader would wait forever on a
  worker that died.
- **Deferred arrival.** An attached participant always calls `finish`, even
  when its build panics. Otherwise the others wait for an arrival that never
  comes.
- **Donated arenas.** A participant's `buildBytes` / `buildCells` become
  dereference-only (`buildCellsShared` is new), because the others probe those
  rows after it closes.
- **Claim-kind coverage.** `TestParallelClaimSetAttachesEveryKind` counts claim
  kinds by reflection. `hashBuildKids` is covered, and the growth mutex is
  counted as bookkeeping, like `setOpKidsOnce`.

Evidence, on a hand-built Gather → Parallel Hash plan (the serial plan with
the join marked):
- identity with serial for inner, LEFT, EXISTS, NOT EXISTS and NOT IN, under
  `-race`;
- `builders=5` (4 workers plus the leader) published 5939 rows, exactly the
  inner's non-NULL keys;
- a spilling build fails loudly, and the barrier protocol has a unit test;
- non-vacuity: removing the build-side claim wiring fails identity.

Gates: units, tpch-spotcheck, sf025 (PLAN-SHAPE same=99), acceptance arm,
fireset. Inert by construction: no production producer sets `ParallelHash`.

Next, slice 2: the planner's `parallel_hash = true` arm and the EXPLAIN
`Parallel Hash` label.

## Slice 2 landed (2026-09-24, `cf02e88b9`): the planner arm and the label

- **Producer.** `addParallelHashJoinPath` is `try_partial_hashjoin_path(...,
  parallel_hash = true)` at `hash_inner_and_outer`'s partial-inner site
  (`joinpath.c:2436-2448`). It pairs the cheapest partial outer with the
  cheapest partial inner, is not used for JOIN_UNIQUE_INNER, and is gated
  by `enable_parallel_hash` (now wired end to end: `PlannerSettings`,
  `costParams`, the session reader, the plan-cache fingerprint).
- **Pricing** (`costsize.c:4160-4260`). Every CPU term takes the per-participant
  inner rows and cost, PG's bucket walk included. Only the table geometry
  sees `inner_path_rows_total` under the combined budget `hash_mem ×
  (workers+1)` (`nodeHash.c:699-707`, `hashJoinInputs.hashGeometryInputs`).
- **Executor-capacity refusals** (M0145-0010 scope (d)). The inner must be a
  partial seq scan (`partialPathDrivingKind` agrees), and the build must fit
  one batch in total and per participant (batching is ledgered).
- **Label.** The join's `Parallel ` prefix follows `Join.ParallelHash`. For a
  join PG sets `parallel_aware` only for `parallel_hash`
  (`create_hashjoin_path`). So goopg's complete-inner partial hash join
  (leader-prebuilt table) is now PG's plain `Hash Join`, retiring R7's
  labelling.
- **Walks.** The Gather registers states through
  `optimizer.ParallelHashJoinsIn`, and the claim walk mirrors
  `attachParallelScan` arm for arm (aggregate, sort, fused NLI outer, partial
  NL/merge outer). `tpch-spotcheck` caught the first cut: Q12's Parallel
  Hash sat below a Partial Aggregate and was never registered. It failed
  loudly (the new `errParallelHashUnregistered`), not with wrong rows.

### Measurement (canonical lanes, same day)

| corpus | match | categories changed |
|---|---|---|
| TPC-H (parallel, `PGSHAPED=1`) | **2 → 3** (Q14 joins Q6, Q11) | parallelism 16 → **11**, join-method 11 → 14, scan-type 11 → 13, aggregation 4 → 3 |
| TPC-DS SF0.25 | **2 → 4** (Q93, Q98 join Q9, Q41) | parallelism 81 → **74**, rendering 26 → 24 |

Per query, TPC-H:
- Q14 now MATCHes, the named witness.
- Q7, Q9, Q10 and Q19 leave `parallelism`.
- **Q12** gains join-method, join-order and scan-type; **Q21** gains
  join-method; **Q4** trades aggregation-strategy / join-order for join-method /
  scan-type. Filed as M0146-0002a, not reverted (R3).
- Q16 keeps `parallelism`, so slice 3 finds out why its build over `part`
  still differs.

Values: the sweep is 96/96 with no mismatches or timeouts although 59 SF0.25
plan shapes changed, the acceptance arm is identical, and the fire set is
clean. Raw diffs are in `analysis/m0146/m0146-0002/`.

## Slice 3: Q16 attribution (2026-09-24, no code change)

Q16 still carries `parallelism`, and the cause is not Parallel Hash
capability:

- A planner DP trace of Q16 on the private TPC-H lane shows the
  `parallel_hash = true` path **filed** for `{part+partsupp}` in both
  directions. It is the cheapest partial path: 27131, against 29388 for the
  complete-inner variant (outer `partsupp`).
- The join search covers only `{part+partsupp}`. goopg's legacy unnest turns
  `ps_suppkey NOT IN (SELECT s_suppkey …)` into an anti join outside the
  search. PG keeps it as a `(hashed SubPlan)` filter on the scan:
  `pull_up_sublinks` converts ANY and EXISTS only (`prepjointree.c:665/731`).
- At the search root, a Gather over the partial path (≈39.9k with setup and
  transfer) loses to the serial hash (37118). The Gather above the anti join
  is then added by the post-pass over a serial plan, and the post-pass cannot
  elect a Parallel Hash. PG, with the NOT IN inside the scan, places a Gather
  Merge + GroupAggregate over the whole `part ⋈ partsupp` Parallel Hash.

Owned by M0146-0002b, which also owns the 2026-09-21 ledger row on the NOT IN
→ anti conversion.
