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

---

## 9. MEASURED (2026-09-07)

All arms on one binary built from `866e6fe7e` in a clean worktree, one
`GOOPG_PARTIAL_AGG_PATHS` value per arm, fresh capped server per arm.

**Measurement hygiene note.** The canonical TPC-H cluster (`:65433`,
`bench/tpch/runtime_goopg/data`) was in concurrent use by two other agents
throughout this session — a "repair" server, then a C-17 acceptance gate, both on
that datadir and port. Two of this slice's early arms collided with theirs
(their `goopg stop` on the shared datadir killed this slice's servers, and one
digest ran against their binary before the collision was noticed). Those arms
are discarded. Everything reported below was re-run on a PRIVATE clone of the
cluster at `/tmp/claude-1000/c19g-tpch` on port 5534, verified at the canonical
`lineitem` count of 6,001,255.

### 9.1 Values — the failure mode a digest of counts cannot see

| gate | arm | result |
|---|---|---|
| TPC-H `tpch-runner -digest` | off | 24/24 OK |
| TPC-H `tpch-runner -digest` | **on** | 24/24 OK |
| TPC-H `tpch-runner -diff off on` | — | **24 MATCH, PASS on VALUES** |
| TPC-DS SF0.5 full sweep | **on** | **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4** |

The TPC-DS report's flag-provenance line carries
`GOOPG_PARTIAL_AGG_PATHS=on`, so the arm is self-identifying
(`bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260907-041738.txt`).
Its own channels: `verdict-changes=none`, `runtime-moves=0`,
TOTAL 1116 s → 1072 s (**−3.9%**), plan-shape `same=98 changed=1` (Q21).

### 9.2 Plans

- Mode **off**, `make plan-gate` against the pin `plan_snapshots/c05-c04b-20260907.txt`:
  **22/22 MATCH.** The serial control arm is inert against the shared pin, not
  merely inert by unit test.
- Mode **on**: 3/22 diverge — **Q1, Q5, Q9**, each by gaining
  `Finalize → Gather → Partial` with a `Group Key`. Those are exactly the
  grouped aggregates the retired size rule refused: `aggColumnStats` could not
  reach their group keys, so `groupsToRowsRatio` returned "cannot estimate" and
  the split was declined outright. Before this slice the ONLY aggregates that
  split on TPC-H were the three UNGROUPED ones (Q6, Q14, Q19), because an
  ungrouped aggregate is the one case `groupsToRowsRatio` answers without
  statistics.
- `MODE=costs` is **NOT EVALUABLE** on the private clone and is not claimed as a
  pass: a restarted clone loses `TableStats.RowCount`, so the cost columns drift
  wholesale — the mode-OFF control arm diverges 21/22 on the same server where
  structural is 22/22. It needs the canonical cluster in its pinned-stats regime
  (the "plan pin had THREE stats drift sources" result), which was contended.

### 9.3 Timing — three alternating passes per arm, medians

Each pass is a fresh server; arms alternate off/on/off/on so server age and
machine state track together. Machine load ~4 throughout (peer agents active),
so the noise floor is the stated ±17%.

| query | off (s) | on (s) | median Δ |
|---|---|---|---|
| **Q1** | 8.54, 8.64, 8.57 | 3.31, 5.14, 4.14 | **−51.7%** |
| Q5 | 4.05, 4.66, 4.28 | 4.90, 4.59, 4.25 | +7.2% |
| Q9 | 3.95, 4.18, 4.02 | 3.76, 4.06, 3.87 | −3.7% |
| Q13 | 5.03, 5.44, 5.30 | 5.52, 5.43, 5.41 | +2.5% |
| Q19 | 2.71, 2.26, 2.36 | 2.27, 2.48, 2.25 | −3.8% |
| Q21 | 16.53, 14.30, 14.18 | 15.70, 14.59, 14.47 | +2.0% |
| full suite (1 pass) | 143.88 | 137.68 | −4.3% |

**Q1 is the result.** Its off-arm spread is 8.54–8.64 s — 1.2% — so a −51.7%
median move is two orders of magnitude outside the noise. It is also exactly the
query the split was built for and the one the retired rule could not reach:
5.9 M rows into four groups, funnelled through a Gather into ONE leader-side
aggregate. Q5's +7.2% and Q14's +10.6% (0.47 s → 0.52 s) both sit inside their
own arms' spreads and are not claimed as regressions in either direction.

### 9.4 Does a Gather become choosable? Yes — and the arithmetic

For Q1 at 4 workers, with `partialGroups = 4` and `d = 4`:

- **what crosses**: `Gp = partialGroups × d =` **16 group-states**, against
  `R =` **5,901,255 rows** for the no-split arm — a factor of 3.7 × 10⁵;
- **charged**: `parallel_tuple_cost × 16 = 1.6`, against
  `parallel_tuple_cost × 5.9 M = 590,000`;
- **saved on top**: `cpu_operator_cost × (A+K) × R × (1 − 1/d) ≈ 110,000`.

So the split candidate beats the gathered one by ≈ 7 × 10⁵ cost units, and the
crossover of §3.4 is satisfied by six orders of magnitude. This is the answer
C-19d §5.1 could not get at a base rel (0.1/row charged against 0.0075/row
saved) and C-19f could get only conditionally (`N > 106,667 + 9.87·J`): partial
aggregation does not merely shift the crossover, it removes the boundary term
almost entirely, because goopg's Partial node emits no rows at all.

### 9.5 The default — recommendation, and why it is not flipped here

Every criterion in §6.4 for a POSITIVE result is met: both values gates are
clean, the win is 50× outside the noise floor on the query the mechanism exists
for, nothing regresses outside its own arm's spread, and the mode-off control is
22/22 against the shared plan pin.

**Recommendation: flip `GOOPG_PARTIAL_AGG_PATHS` to `on` by default.**

It is NOT flipped in this slice's commits for two reasons, both about shared
state rather than about the evidence:

1. Flipping moves three TPC-H plans (Q1/Q5/Q9), so it requires re-pinning
   `plan_snapshots/` in the same commit — as C-15 did. That pin is a shared
   artefact and two peer agents were mid-A/B against it for the whole session;
   re-pinning under them would silently move their baselines.
2. `MODE=costs` has not been run in its pinned-stats regime (§9.2), and the
   canonical cluster needed for it was contended and is currently in a
   WAL-early-end state.

The flip is therefore a small, fully specified follow-up: set the default arm of
`partialAggModeFromEnv`, run `make plan-gate` + `MODE=costs` on the canonical
cluster, re-pin `plan_snapshots/` in the same commit, and regenerate
`scripts/planner-flags.env` (the label becomes `unset(on)`).

---

## 10. THE REMAINDER, LANDED (2026-09-07) — §8's upper-rel-resident half

§8 named one change in `groupingpaths.go` plus "the plan-cache question of
§2.2". Both are done, and the item's `[~]` closes. New file:
`internal/optimizer/partialaggupper.go`; the DEFAULT is now
`GOOPG_PARTIAL_AGG_PATHS=on`.

### 10.1 Why it had to be finished before anything else in Phase 5 could move

C-19g replaced the split VERDICT but not its CONSTRUCTION: `splitAggregate`
(parallel.go) still built `Finalize → Gather → Partial` inside the
`MaybeAddGather` POST-PASS, so C-19g's own Q1 win was delivered THROUGH the
post-pass and died with it. C-19h measured the consequence
(`docs/design/planner-c19h-gather-postpass/DESIGN.md` §3): at the engine
defaults the post-pass was the ONLY producer of parallelism — 12/22 TPC-H
queries with it, **0/22** without — and a stand-down probe under
`GOOPG_GATHER_PATHS=all GOOPG_PARTIAL_AGG_PATHS=on` reached only 7/22, losing
Q1, Q6, Q14, Q15a, Q16 and Q19. Every one of those six is an aggregate query.

### 10.2 §2.2's two blockers, answered

**"No input rel."** Upstream seeds `partially_grouped_rel` from
`input_rel->partial_pathlist`, and the rel carrying `PartialPathlist` dies
inside `planJoinlistSearch` before the aggregate stage runs. The answer is that
goopg does not need a distinct partial PLAN: `gatherOp.runWorker` builds each
worker's own copy of the Gather's child subtree and calls `attachParallelScan`
on it, so the partial plan IS the serial subtree with its driving scan stamped
— which is what `splitAggregate` has always relied on. What upstream's partial
path supplies that a Node cannot is the PRICE, and that is supplied by
`parallelSeedCost`: startup unchanged, RUN cost divided by
`get_parallel_divisor`. That is `cost_seqscan`'s own parallel adjustment
applied to a subtree rather than to one scan, and it is the single quantity in
the producer that is not transcribed from a PG cost function. Its direction is
known and stated: upstream leaves the per-page I/O term undivided because the
workers share one relation, so dividing the whole run cost OVERSTATES the
speedup of an I/O-bound subtree.

**"The parallel decision may not be CACHED."** Answered in two halves, both
landed here:

1. the parallel block now reaches the pre-cache planner
   (`plannerSettingsFrom`) and is part of `plannerCacheFingerprint`
   (`max_parallel_workers_per_gather`, `min_parallel_table_scan_size`,
   `min_parallel_index_scan_size`, `parallel_leader_participation`,
   `enable_gathermerge`), so a session with its own parallel settings keys
   into its own cache entry instead of borrowing a plan built under someone
   else's;
2. the inputs that are NOT session GUCs — the transaction's isolation level,
   and the statement-shape refusals `statementIsParallelSafe` makes — are
   enforced AFTER the lookup by the new `StripGather` (parallel.go), which
   `MaybeAddGather` now calls on any plan it is not allowed to parallelise
   instead of returning it unchanged. `StripGather` is exact rather than
   approximate in both shapes it meets: a plain Gather is semantically
   transparent, and a split aggregate folds back to the SIMPLE aggregate it
   was split from — dropping only its Gather would leave a Finalize over a
   node that emits no rows at all.

### 10.3 The producer

`addPartialAggSplitPath` files, on the SAME `GROUP_AGG` rel C-15's serial
candidates sit on, adjudicated by the same `addPath`/`setCheapest`:

- **the split** — `Finalize → Gather → Partial`, one `PathFinalizeAgg` whose
  `createPlan` arm calls `splitAggregate` itself, so a path-model split and a
  post-pass split are byte-identical rather than merely similar (rule #2);
- **the gathered no-split family** — `Agg → Gather → input`, in the hashed and
  sorted shapes `addGroupingPaths` offers, built as if no usable presorted keys
  existed (a Gather interleaves its workers' streams, so input order is gone
  above it).

The no-split family is not optional, and TPC-H proved it twice. Without it the
serial arms are the only competition, they are priced over the UNDIVIDED input,
and the split therefore wins on the parallel divisor alone even where it
pre-aggregates nothing: Q3 and Q10 (≈300 k groups from ≈300 k rows) both took a
split that reduced nothing. And gating the whole producer on
`aggregateSplitIsSafe` — rather than gating only the split arm — cost Q16 its
Gather entirely: a `count(distinct …)` cannot be split, but the Gather still
belongs below it, which is exactly what `terminatesPartial` makes the post-pass
do.

Worker sizing is `computeParallelWorkerForRel` — the path model's own entry,
not the post-pass's `computeParallelWorkers`, which needs a live block count
through `ParallelSettings.BlocksForTable` and returns 0 without one. The live
size comes from the new `catalog.TableRealPages`, `IndexRealPages`' sibling and
PG's own input (`estimate_rel_size` fills `rel->pages` from
`RelationGetNumberOfBlocks`). Keying it on ANALYZE statistics instead would
have refused every query on a freshly started server, since
`TableStats.RowCount`/`Pages` are not restored (ledger pq-P6).

### 10.4 Four defects the live gates found, each a general rule

1. **A Finalize node is not stampable.** `stampAggregateInputTarget` derived a
   keep from the Gather's output row while the GroupExprs still addressed the
   input row; `assertAggregateInputTargetCoversKeys` stopped the process on
   nine TPC-H queries. `splitAggregate` already declined this on the node it
   built; the producer made a Finalize reach the caller's re-stamp. Fixed by
   declining for any non-Simple aggregate.
2. **Passes that run after the aggregate stage must descend through a Gather.**
   Before this slice the only Gather producer ran AFTER all of them, so
   `foldPlanConstants` and `walkPlanExprs` had no Gather arm. The fold's
   absence was not cosmetic: EXPLAIN recomputes `rows=` from the predicate, so
   Q6 read `rows=53603` against the folded plan's `2412` for the identical
   scan.
3. **A nested scope must be closed BY DEFAULT, not by enumeration.** The
   statement-shape flag was first derived inside `PlanWithSettings` from the
   statement's own type — and a VIEW BODY is planned by a re-entrant `Plan`
   call (planner.go:3440) whose result becomes a LEAF of the enclosing
   statement's join search. The body looked like a top-level SELECT, came back
   carrying a Gather, and `assertSearchedTreeNeedsNoReconcile` stopped Q15b.
   The flag now travels one way only: raised by the postmaster's top-level
   sites, cleared for every other statement shape and every nested scope.
4. **EXPLAIN must be transparent to that flag.** With `*parser.ExplainStmt`
   clearing it, EXPLAIN planned a serial aggregate for a statement that
   executed the split — the census read 0/22 queries carrying a Gather while
   the digest arm's Q1 ran 43% faster. EXPLAIN is the only way a user OBSERVES
   a plan; of all the recursive entry points it is the one that must not
   default.

### 10.5 MEASURED (2026-09-07, private clone of the SF=1 cluster on port 5541)

One binary per campaign, fresh capped server per arm, `GOOPG_ANALYZE_SEED=20260905`,
`GOMEMLIMIT=12GiB GOGC=off`.

**The control arm is inert, cost-exactly.** With the code landed and the knob
`off`, `make plan-gate` against the previous pin `c05-c04b-20260907` is
**22/22 MATCH in BOTH `structural` AND `MODE=costs`** — the cost-exact control
C-19g could not obtain on its own clone.

**Plans.** With the default flipped, three queries move against that pin, and
the new pin `plan_snapshots/c19g-upper-partialagg-20260907.txt` is 22/22 in
both modes:

| query | move |
|---|---|
| Q5 | gains `Finalize → Gather → Partial` (25 groups) |
| Q9 | gains `Finalize → Gather → Partial` (303 093 rows → 5 000 groups) |
| Q16 | `GroupAggregate` over `Sort` over `Gather` → `HashAggregate` over `Gather` |

Q1 MATCHES the pin: the split it already had is now produced by the PATH rather
than by the post-pass, which is the whole point of the slice and is what §10.6
measures.

**Values.** TPC-H `tpch-runner -digest`, four arms (off/on/off/on):
**24 MATCH, PASS on VALUES** in every pairing. TPC-DS SF0.5 sweep: see §10.7.

**Timing**, two passes per arm, host load 3–14 (peer agents active), so the
per-query noise band is the stated ±17%:

| query | off (s) | on (s) | mean Δ |
|---|---|---|---|
| **Q1** | 14.54, 10.52 | 8.29, 6.50 | **−41.0%** |
| Q16 | 0.83, 0.84 | 0.48, 0.61 | −34.7% |
| suite total | 156.8, 151.8 | 138.6, 148.8 | −6.9% |

An earlier, quieter campaign (load ≈4) on the same clone read Q1 8.49/8.82 →
4.46/5.28 (**−43.7%**) and a suite total of −4.2%. The suite delta sits at the
edge of the arms' own spread and is not claimed; Q1 and Q16 are outside it.

### 10.6 The C-19h census, re-run — C-19h is UNBLOCKED

Same engine image, engine defaults, EXPLAIN over the 22 TPC-H queries; the
probe build stands `MaybeAddGather`'s ADD half down while keeping its
`StripGather` enforcement.

| arm | queries carrying a Gather |
|---|---|
| post-pass live, knob **off** (the pre-flip default) | **12/22** |
| post-pass live, knob **on** (the new default) | **12/22** |
| post-pass **stood down**, knob **off** | **0/22** |
| post-pass **stood down**, knob **on** | **12/22** |

The twelve are the SAME twelve in every non-zero arm: Q1, Q3, Q5, Q6, Q7, Q8,
Q9, Q10, Q14, Q15a, Q16, Q19. C-19h's blocker — "at the default the post-pass
is the ONLY producer of parallelism, 12/22 with it and 0/22 without" — no
longer holds: the path model reaches every query the post-pass reaches, and the
six-query loss cohort (Q1, Q6, Q14, Q15a, Q16, Q19) is fully recovered.

Retiring the post-pass is still NOT taken here. Two things must survive it and
neither is C-19g's to move: the `StripGather` enforcement (the post-cache half
of the plan-cache answer above, which is not a "post-pass" at all and must
outlive the ADD half), and `debug_parallel_query`, which the upper-rel producer
does not read — a forced-parallel regress arm still needs the post-pass or an
equivalent path-model gate.

### 10.7 Gates

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — green.
- `go vet ./internal/optimizer/ ./internal/postmaster/` — clean.
- `make plan-gate` and `make plan-gate MODE=costs` against
  `c19g-upper-partialagg-20260907` — 22/22 both.
- TPC-H digest — 24/24 MATCH on VALUES, both arms, twice.
- TPC-DS SF0.5 sweep — recorded in the TODO_ALL row.
