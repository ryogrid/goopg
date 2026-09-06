# C-19g (P5-07) — partial aggregation as PATHS

Status: design, written before implementation (2026-09-07).
Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`, row `C-19g P5-07`.
Parent design: `docs/design/not_ralph/planner_refactor_take3/08-target-design.md` §8.
Gate protocol: same bundle, `09-…` §5 row **P5** (PP + values-diff).
Upstream oracle (read-only): `postgres/` @ PG 18.3.
Predecessors: C-19d (`docs/design/planner-c19d-gather-paths/DESIGN.md`),
C-19f (`docs/design/planner-c19f-parallel-hashjoin/DESIGN.md`), C-15
(`docs/design/planner-p4-grouping-paths/DESIGN.md`).

---

## 1. The chain's arithmetic, and why this is the item that can pay

C-19d landed `PathGather` / `PathGatherMerge` priced by `cost_gather` and shipped
default-OFF for an arithmetic reason, not caution (that doc §5.1): with only
BASE-rel partial paths the whole relation crosses the boundary, so the charge is
`parallel_tuple_cost` = **0.1/row** against a 4-worker saving of
`cpu_tuple_cost`'s share ≈ **0.0075/row**. `add_path` correctly dominates every
Gather at any relation size.

C-19f put the JOIN below the boundary. The crossover `N > 106,667 + 9.87·J`
became satisfiable for a join tree (Q21 17.33 s → 8.42 s, −51%), but Q9 +94% and
Q10 +30% kept the default off.

C-19g is the aggregation half of the same argument, and in goopg it is
**qualitatively** stronger than in PG, because goopg's partial aggregate emits
**no rows at all**:

> "Publish this worker's groups and emit NOTHING. The Finalize node supplies
> every output row from the accumulator, so a Partial node returning zero rows
> is by construction, not a failure"
> — `internal/executor/operators_join_agg.go:2351-2372`

The per-group states cross through a mutex-guarded accumulator
(`aggPartialAccum`, published by the Finalize before the child Gather opens,
`operators_join_agg.go:2170-2178`), not through the Gather's row stream. So what
crosses the boundary under a split aggregate is `numGroups × workers`
group-states instead of `inputRows` tuples — on TPC-H Q1 that is **16 versus
5.9 M**, five and a half orders of magnitude. This is the term that makes a
Gather affordable, and §4 turns it into the crossover inequality.

## 2. What is actually being replaced, and where it can legally live

### 2.1 `splitAggregateIsProfitable` is a size rule with invented constants

`parallel_agg.go` decides split-vs-no-split with five constants — `cXfer = 2.0`,
`cTrans = 1.0`, `cHash = 0.25`, `cMerge = 4.0`, `cOut = 1.0` — whose own comment
says they are "calibrated against one query" (chapter 11 §3.3). None of them is a
PG cost constant, none is reachable from a GUC, and the ratio input
(`groupsToRowsRatio` → `aggColumnStats`) **refuses to descend through a Join or
a Project**, so every join-fed aggregate — which is most of TPC-H — falls into
`ok == false` and is refused outright. Refusal means the Gather drops BELOW the
aggregate and the whole join output funnels into one leader-side aggregate: the
exact serial tail the split exists to remove.

That is the defect Phase 5 removes: a structural rule standing in for a price.

### 2.2 Why this cannot be a `create_partial_grouping_paths` port inside C-15

Upstream builds `partially_grouped_rel` inside `add_paths_to_grouping_rel`
(`planner.c:4092`, producer at `planner.c:7351`), seeded from
`input_rel->partial_pathlist` (`planner.c:7386-7388`), gathers it
(`gather_grouping_paths`, `planner.c:7704`) and files `AGGSPLIT_FINAL_DESERIAL`
paths onto `grouped_rel` (`planner.c:7212-7266`) — all in one place, all inside
the planner.

goopg cannot do that today, for **two independent reasons**, and both are
load-bearing rather than incidental:

1. **No input rel.** C-15's `createGroupingPaths` (groupingpaths.go) receives a
   finished `*Aggregate` **Node**, not the search's `RelOptInfo`. The rel that
   carries `PartialPathlist` — the thing C-19b/c/f populate — dies inside
   `planJoinlistSearch` before the aggregate stage runs. There is no
   `input_rel->partial_pathlist` to read.
2. **The parallel decision may not be cached.** `MaybeAddGather` runs AFTER the
   plan-cache lookup, and `internal/postmaster/dispatch.go:1480-1490` states why:
   the cache is process-wide and keyed on `(dbOid, normalised SQL)` with no GUC
   fingerprint, so a plan built under `max_parallel_workers_per_gather = 4` and
   cached would be served to a session that set it to 0 — `SET … = 0` would
   silently fail. `ParallelSettings` is deliberately excluded from
   `sessionPlannerFingerprint` (dispatch.go:1967). C-15's producer runs
   **pre**-cache. Moving the split decision there would re-open that hole.

So the split decision belongs at the post-cache site, and C-19g's content is
making that decision a **priced path comparison** instead of a size rule. The
upper-rel-resident port stays open as the remainder (§8).

### 2.3 Ownership

`groupingpaths.go`, `upperrel*.go`, `planner.go`, `createplan*.go` and
`cost_funcs.go` belong to the concurrent C-17/C-18 agent. This slice adds
`internal/optimizer/partialaggpaths.go` and edits `parallel.go` /
`parallel_agg.go` only. It reuses `costAgg`, `gatherCost`, `getParallelDivisor`,
`estimateNumGroups`, `addPath`, `setCheapest` and `fetchUpperRel` **as callers**,
changing none of them. §8 names the one change another owner would have to make
for the full port.

---

## 3. The producer

New file `partialaggpaths.go`. `createPartialGroupingPaths` builds a real,
two-rel path tournament and returns the verdict:

```
PARTIAL_GROUP_AGG rel                GROUP_AGG rel
  seed  PathPrebuilt (partial input)
  →  PathAgg  (partial, per-worker)
       →  PathGather                 →  PathAgg (finalize)      ← "split"
                                     →  PathAgg (simple)        ← "no split"
                                          over PathGather(all rows)
```

Both candidates are filed with `addPath` on ONE `GROUP_AGG` rel and adjudicated
by `setCheapest`, so the verdict comes from `compare_path_costs_fuzzily` — the
same comparator, with the same 1% fuzz band, that decides everything else. The
rel and registry are local to the call (`newUpperRels()`, the escape C-15's own
producer already takes for a nil registry), so nothing enters `searchCtx.relMap`
and the invariant `upperrel.go` §3 states is untouched.

### 3.1 The shared input term cancels — so no absolute input price is needed

Both candidates stand on the same partial input path (the same scan/join subtree
run by the same workers). `costAgg`'s total is `input.Total + …` and
`gatherCost`'s total is `sub.Total + …`, both strictly additive in the input, so
the input's total appears exactly once in each candidate and cancels in the
comparison. This is the same cancellation the retired rule relied on ("compares
only the two alternatives that differ … the shared term cancels", parallel_agg.go)
— but here it is a property of the transcribed cost functions rather than a
premise of a bespoke model. The seed is therefore filed with the child Node's own
`legacyDisplayCostOf` price, which is what C-15's seed uses, and its absolute
value cannot change the verdict.

### 3.2 Row estimates — upstream's, not `NDistinctFrac`

| upstream | goopg |
|---|---|
| `cheapest_partial_path->rows` | `inputRows / d`, `d = getParallelDivisor(workers, leaderParticipates)` (cost_funcs.go:175 = `get_parallel_divisor`, costsize.c:6474) |
| `dNumPartialPartialGroups = get_number_of_groups(root, cheapest_partial_path->rows, …)` (planner.c:7452) | `estimateNumGroups(agg.GroupExprs, agg.Child, inputRows/d)` |
| `dNumGroups = get_number_of_groups(root, cheapest_path->rows, …)` (planner.c:4131) | `estimateNumGroups(agg.GroupExprs, agg.Child, inputRows)` |
| `compute_gather_rows(partial_agg_path)` (costsize.c:6625) | `computeGatherRows` semantics: `partialGroups × d` |

`estimateNumGroups` (cardinality.go:1202) is the PG-faithful
`estimate_num_groups` port C-15 already trusts for the GROUP_AGG rel's `Rows`;
it decomposes expressions, handles multi-column independence, and falls back to
`defaultNumDistinct` for what it cannot see — it never REFUSES. Adopting it here
is the single largest behavioural change in this slice: the join-fed and
expression-keyed aggregates the old rule refused outright now get a price.

### 3.3 Cost terms, transcribed

All three arms are existing functions; this slice adds no cost function and no
constant. `cp` is `DefaultPlannerSettings().costParams()` (see §5.3).

**Partial arm** — `create_agg_path(… AGGSPLIT_INITIAL_SERIAL …)`, planner.c:7606:

```
costAgg(cp, strategy, inputRows/d, seed.Startup, seed.Total,
        len(GroupExprs), partialGroups, len(Aggs), …)
```

**Boundary** — `cost_gather` (costsize.c:446) over the partial-agg path:

```
gatherCost(cp, partialCost, partialGroups*d)
```

The output-row argument is `compute_gather_rows` of the partial-agg path, i.e.
the number of GROUP-STATES that reach the leader. In PG these are tuples on a
`shm_mq`; in goopg they are `pub.merge` calls into the shared accumulator under
its mutex (`operators_join_agg.go:2365-2372`). Charging both at
`parallel_tuple_cost` is the one deliberate adaptation in this slice, and it is
the faithful one: `parallel_tuple_cost` is defined as the cost of transferring
ONE tuple from a worker to the leader (costsize.c / `cost.h`), and goopg
transfers one group-state where PG transfers one tuple. It is also the
conservative direction — a mutex-guarded map merge is not obviously cheaper than
a queue write.

**Finalize arm** — `create_agg_path(… AGGSPLIT_FINAL_DESERIAL …)`, planner.c:7250:

```
costAgg(cp, strategy, partialGroups*d, gather.Startup, gather.Total,
        len(GroupExprs), finalGroups, len(Aggs), …)
```

Upstream charges the combine function per INPUT row of the finalize node
(`partialGroups*d`) and the final function per output group. goopg's combines
happen inside `pub.merge` rather than in the leader's Agg loop, but the work is
the same per group-state, so the arm is priced unchanged. Charging it where PG
charges it keeps a single cost model rather than two half-models.

**No-split arm** — the fallback `terminatesPartial(*Aggregate)` actually
produces: `cost_gather` over the whole input, then a simple aggregate.

```
gatherCost(cp, seed.Cost, inputRows)
costAgg(cp, strategy, inputRows, gather.Startup, gather.Total,
        len(GroupExprs), finalGroups, len(Aggs), …)
```

This is not a hypothetical: refusing the split is exactly what
`findPartialSubtree` does today, and the Gather then lands below the aggregate.
The comparison is therefore against the plan that is really built.

### 3.4 The crossover, in the named constants

Writing `R` = input rows, `G` = final groups, `Gw` = partial groups per worker,
`Gp = Gw·d` = group-states crossing, `A` = `len(Aggs)`, `K` = `len(GroupExprs)`,
and `ptc`/`ctc`/`coc` for `parallelTupleCost` / `cpuTupleCost` /
`cpuOperatorCost`, the two totals differ by (the `parallel_setup_cost` cancels —
both shapes place exactly one Gather):

```
split wins  iff
    ptc·(R − Gp)  +  coc·(A+K)·R·(1 − 1/d)          [saved: transfer + per-worker CPU]
  >  coc·A·Gw + ctc·Gw  +  coc·(A+K)·Gp             [paid: partial emit + finalize]
```

Two sanity readings, at PG 18 defaults (`ptc = 0.1`, `ctc = 0.01`,
`coc = 0.0025`) and `d = 4`:

- **TPC-H Q1** (`R = 5.9 M`, `K = 2`, `A ≈ 8`, `G = 4`, so `Gw = 4`, `Gp = 16`):
  LHS ≈ 590,000 + 110,625 = **700,625**; RHS ≈ **0.5**. The split wins by six
  orders of magnitude, which is the correct answer — without it Q1 pins at ~7.1 s
  on the leader-side aggregate.
- **A grouping that does not group** (`Gp → R`, every row its own group): the
  transfer saving vanishes and the paid side becomes `coc·(A+K)·R`, strictly
  larger than the saved `coc·(A+K)·R·(1−1/d)`. The split LOSES. Correct: there is
  nothing to pre-aggregate.

So the rule discriminates, and it discriminates on the right quantity — the
aggregate's reduction factor `Gp/R` — rather than on an `NDistinctFrac` lookup
that is unavailable above a join. Dividing through by `R` and solving for
`g = Gp/R` at `A = 8, K = 2, d = 4` gives the break-even at **g ≈ 0.90**: the
split is preferred unless the aggregate reduces the row count by less than 10%.
At `d = 1.7` (one worker, leader participating) the same substitution gives
`g ≈ 0.60`, so the gate genuinely tightens as the worker count falls, which the
old constant model could not express at all.

---

## 4. Does a Gather become choosable? The honest answer, both ways

**Within the post-pass (this slice's live surface): yes, and it already was.**
`parallelOn` defaults on, and the post-pass already builds
`Finalize → Gather → Partial` when the size rule accepts. What changes is WHICH
aggregates get it: the old rule refused every aggregate whose group keys it could
not resolve through `aggColumnStats` (no Join, no Project descent), which is
most of the suite. The new rule prices them. That is a plan change on the
DEFAULT path, so §6's gate battery is mandatory and the knob (§5.2) exists so the
change can be measured against itself.

**Within the path model (`GOOPG_GATHER_PATHS`): no, not from this slice alone.**
The path-model Gather is produced at the search's rels (base and, since C-19f,
joinrels). The aggregate is built ABOVE the search seam, so no partial
aggregation is visible to `generateUsefulGatherPaths` and the rows that cross a
path-model Gather are still the join tree's full output. C-19d §5.1's
inequality is therefore unchanged by this slice, and this slice does not move
`GOOPG_GATHER_PATHS`. Closing that gap is §8's remainder and needs the upper-rel
port — i.e. an owner of `groupingpaths.go`. Saying so with the arithmetic is the
result, in the same sense C-19d's negative was a result.

---

## 5. Admission, coexistence, safety

### 5.1 `MaybeAddGather` coexistence — unchanged from C-19d, plus one rule

C-19d's rule stands verbatim: **if the tree already carries a `*Gather` or
`*GatherMerge`, the post-pass returns the root unchanged** (`subtreeHasGather`,
parallel.go:190). That is a correctness stop, not tidiness —
`findPartialSubtree` descends THROUGH a terminating single-child node, so without
it the post-pass would nest a second Gather below the path model's, N workers
each launching N.

C-19g adds nothing that can double-split, and the reason is structural: the new
producer is called from exactly ONE site, `findPartialSubtree`'s existing
`*Aggregate` arm, in place of `splitAggregateIsProfitable`. It returns a boolean
verdict and constructs no node; `splitAggregate` (parallel.go:868) still builds
`Finalize → Gather → Partial`, unchanged, non-mutating, with the shallow copies
the plan cache requires. There is no second producer and no second construction
site, so "how is double-splitting prevented" has the strongest possible answer:
nothing else builds a split.

### 5.2 The knob

`GOOPG_PARTIAL_AGG_PATHS` — `off` (the retired size rule) / `on` (the priced
path tournament), read once at process start, resolved through the same
fail-closed `switch` shape `GOOPG_GATHER_PATHS` uses, registered in
`flaglabels.go` so every benchmark artefact names its arm, and exported as
`SetPartialAggPathsMode` for the executor consumer check (which lives in
`internal/executor` and cannot reach an unexported knob — the same reason C-19d
exported `SetGatherPathsMode`).

**The default is a MEASURED decision** and §6 is the measurement. Landing at
`off` and flipping in a second commit, or landing at `off` permanently with the
arithmetic written down, are both acceptable outcomes; flipping on an unmeasured
or within-noise result is not.

### 5.3 Cost params at a post-cache site

`MaybeAddGather` carries `ParallelSettings`, not `PlannerSettings`, so the
producer takes `DefaultPlannerSettings().costParams()`. This is honest rather
than ideal, and it is strictly better than what it replaces: the retired rule
used five hardcoded non-GUC constants, this uses PG's GUC defaults through the
named `costParams` fields. Session `cpu_operator_cost` etc. do not reach the
post-pass — the same gap `costParams.workMem`'s comment records (ledger
M0127-P5.7-a), and the same one C-19h will close when the parallel block reaches
`plannerSettingsFrom`. Recorded, not hidden.

### 5.4 Decomposability — fail-CLOSED, and unchanged

`considerparallel.go` is fail-closed by hard-won design (a review found four
fail-open holes). This slice admits **no new node shape** into the parallel path
search: it changes a boolean gate on a shape `findPartialSubtree` already
accepted. The decomposability guard is untouched and still runs FIRST:

```
if agg, isAgg := cur.(*Aggregate); isAgg && aggregateSplitIsSafe(agg) &&
        drivingScan(agg.Child) != nil {
        if <verdict> { … }
}
```

`aggregateSplitIsSafe` (parallel_agg.go:113) refuses non-Simple mode, grouping
sets, agg-less group-only nodes, and any node containing a call
`AggregateIsDecomposable` does not whitelist (DISTINCT, internal ORDER BY, user
aggregates, and everything absent from the name whitelist — a whitelist
deliberately, because the executor's `applyAgg` ends in a `default:` catch-all
that would silently return garbage for a name added later). The new producer
asserts `aggregateSplitIsSafe` again at its own entry and returns "no split" if
it does not hold, so the gate cannot be bypassed by a future second caller. An
aggregate that is not decomposable is never priced, let alone split.

`drivingScan(agg.Child) != nil` likewise stays: the executor's `runWorker`
IGNORES `attachParallelScan`'s return value, so an unmodelled subtree does not
"stay serial" — every worker reads the whole relation.

---

## 6. Acceptance argument, and what a negative result looks like

### 6.1 Unit (no server)

1. The crossover is asserted **through the named `costParams` fields**, never a
   literal — both directions, so what is pinned is the inequality of §3.4 and not
   a calibration. (A literal once put a crossover test inside `add_path`'s 1%
   fuzz band, and separately pinned a stale multiplier worth 27% of the suite.)
2. **BOTH candidates must be shown to exist before any cost is compared.** Five
   hypotheses were burned on Q8 because a producer emitted nothing at that
   parameterisation. The producer therefore returns its pathlist for inspection,
   and the tests assert the GROUP_AGG rel holds exactly two paths — split and
   no-split — before asserting which won.
3. Q1's shape (large `R`, tiny `G`) prefers split; the degenerate shape
   (`Gp → R`) prefers no-split; the one-worker case tightens.
4. A non-decomposable aggregate is refused before pricing.
5. Mode `off` reproduces the old verdict exactly (serial control arm).

### 6.2 Executor consumer check — mandatory

"An unwinnable path is an untested path" has fired four times in this workstream;
C-19f's consumer check found two `createPlan` bugs unreachable until a Gather
could win, and E-10 found a Gather Merge returning `(workers+1)×` every row IN
THE CORRECT ORDER, which only a VALUES test caught. So: a fixture in
`internal/executor` where the split verdict is reached by cost must actually
execute as `Finalize → Gather → Partial`, and must return the **same values** the
serial plan returns — counts alone cannot see a duplicated or dropped group.

### 6.3 Suite gates

- `go build ./...`, `go vet`, full `go test ./internal/optimizer/ ./internal/executor/` (never `-count=1`).
- `go test -race` on the parallel set. Known pre-existing failure, not this
  slice's: `TestSubquerySemanticsMatrix/M20` races on the package-global
  `instrumentScope` (`instrument.go:374` vs `:435`), ledger
  `take3-instrumentscope-datarace`. Any other race is this slice's.
- TPC-H values **24/24** via `tpch-runner -digest` / `-diff`, **plus a
  values-diff arm with the mode ON** — a partial aggregation that loses rows is
  the failure mode, and it is invisible to a digest of counts.
- **TPC-DS SF0.5 full sweep `PASS=95 MISMATCH=0 CKMISMATCH=0 TIMEOUT=0`.** In the
  last day this gate caught a wrong answer and a 20× timeout that TPC-H passed
  clean.
- `make plan-gate` + `MODE=costs`, pin `plan_snapshots/c05-c04b-20260907.txt`.
- Timing: pre-change binary built and run in the SAME session, fresh capped
  server per arm, never under a concurrent sweep. A same-session A/A on an
  unchanged binary has shown 6.3% drift and per-query noise is ±17%; a
  within-noise result is not a result.

### 6.4 What a negative result looks like

Concretely, any of:

- the new verdict changes no plan on either suite (the old rule already accepted
  everywhere it mattered) — then the slice is a correctness/clarity refactor,
  the constants are retired, and the default flips only because the two agree;
- the new verdict splits aggregates the old rule refused and the suite gets
  SLOWER — then the priced model is right and the executor's merge is more
  expensive than `parallel_tuple_cost` says, which is a measurement about
  `pub.merge` and a reason to keep the mode off and record the constant;
- TPC-DS finds a values mismatch — then the decomposability gate has a hole and
  the mode stays off until it is named.

Any of those is written up with its arithmetic, as C-19d's negative was. What is
NOT acceptable is flipping the default on a within-noise timing delta.

---

## 7. Files

- `internal/optimizer/partialaggpaths.go` — new: the producer, the knob, the
  verdict.
- `internal/optimizer/parallel.go` — one call site swapped in
  `findPartialSubtree`.
- `internal/optimizer/parallel_agg.go` — the retired rule kept behind the knob's
  `off` arm as the serial control, with its constants labelled retired.
- tests beside each.

## 8. The remainder (NOT this slice)

The upper-rel-resident port — `PARTIAL_GROUP_AGG` seeded from the search's
`PartialPathlist`, `gather_grouping_paths`, and `AGGSPLIT_FINAL_DESERIAL` paths
competing on the GROUP_AGG rel — needs ONE change in a file this slice does not
own: `addGroupingPaths` (groupingpaths.go) must call a partial-grouping producer
and file its finalize path with `addPath`, and `createGroupingPaths` must receive
the search's final `RelOptInfo` instead of only the finished child Node. It also
needs the plan-cache question of §2.2 answered — either by adding the parallel
block to `sessionPlannerFingerprint` or by keeping the split decision post-cache.
Reported to the owner of `groupingpaths.go` rather than made here.
