# C-19d (P5-04) — `generate_useful_gather_paths`: Gather as a PATH

Status: design, written before implementation (2026-09-06).
Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`, row `C-19d P5-04`.
Parent design: `docs/design/not_ralph/planner_refactor_take3/08-target-design.md` §8.
Gate protocol: same bundle, `09-…` §5 row **P5**.
Upstream oracle (read-only): `postgres/` @ PG 18.3.

---

## 1. What this slice is for

goopg's cost model has **no parallel dimension**. `MaybeAddGather`
(`internal/optimizer/parallel.go`) is a post-planning SIZE rule that runs on a
finished node tree, after the plan cache, and it can only place a Gather where
`drivingScan` recognises a driving scan — in practice a scan, or a hash join
through its probe side. Nothing in the search can *prefer* a plan because that
plan will parallelise.

The measured consequence is recorded in the deferral ledger and in
`analysis/minimize-datum/d05-buildcost-20260906`: three individually CORRECT
hash-join cost corrections (entry width 194→120 B/row; `hashsize.MapSlotBytes`
48→96, itself a measured figure; a 5× private-build multiplier) each cost
between +10% and +22% of the TPC-H suite — Q5 +444%, Q10 +221%, Q9 +115% —
because the plan moved off the hash-join shape and therefore LOST its 5-worker
Gather. The corrections were right; the search could not see what it was
trading away. Track D (D-05) is blocked on this, not on its own cost terms.
The three patches are preserved at `tmp/d05p2-bucket-charge.patch`,
`tmp/d05p3-costside-narrow.patch`, `tmp/d05p4-buildcost.patch`.

C-19a/b/c made parallelism *visible* to the search: `consider_parallel` per rel,
partial seq-scan paths, partial index paths — all in `RelOptInfo.PartialPathlist`,
which to date has **no reader**. C-19d is that reader: it turns a partial path
into an ordinary, priced candidate on the rel's serial `Pathlist`, so `add_path`
decides parallel-vs-serial the way it decides everything else.

---

## 2. The two path kinds

`PathGather` and `PathGatherMerge` already exist in the `PathKind` enum
(`path.go:60-61`) and are already handled (by DECLINING) in `narrowoutput.go`.
Nothing constructs them. This slice constructs them.

Neither needs a new field on `Path`:

| PG (`pathnodes.h`) | goopg carrier |
|---|---|
| `GatherPath.subpath` / `GatherMergePath.subpath` | `Path.Children[0]` (exactly one child) |
| `GatherPath.num_workers` / `GatherMergePath.num_workers` | `Children[0].ParallelWorkers` — by construction `subpath->parallel_workers` (`create_gather_path`, pathnode.c) |
| `path.parallel_safe = false`, `parallel_workers = 0` | `Path.ParallelSafe = false`, `Path.ParallelWorkers = 0` — a Gather is the parallel/serial boundary, not a partial path |
| `path.pathkeys` | `Path.Pathkeys`: empty for Gather ("the output of Gather is always unsorted"), the subpath's list for Gather Merge |
| `path.rows` | `Path.Rows = compute_gather_rows(subpath)` |
| `path.disabled_nodes` | `Path.DisabledNodes` (see §3) |
| `GatherPath.single_copy` | **not modelled** — refused, see §4.3 |

Reading `num_workers` off the child rather than adding a field is deliberate:
two places that can disagree about the worker count is exactly the class of bug
the `Path.Rows`/`ppi_rows` comment in `path.go` warns about, and the child path
is in scope at every site that needs the number (costing and `createPlan`).

---

## 3. Cost formulas (transcribed, with citations)

### 3.1 `compute_gather_rows` — `costsize.c:6625`

```
clamp_row_est(path->rows * get_parallel_divisor(path))
```

goopg: `computeGatherRows(sub *Path, cp costParams)`, over the existing
`getParallelDivisor` (`cost_funcs.go:167`) and `clampRowEst`. A partial path's
`Rows` is already the PER-WORKER count (`costParallelSeqscan` divides by the
divisor), so the Gather multiplies it back.

### 3.2 `cost_gather` — `costsize.c:446`

```
startup = subpath->startup_cost + parallel_setup_cost
run     = (subpath->total_cost - subpath->startup_cost) + parallel_tuple_cost * rows
disabled_nodes = subpath->disabled_nodes
```

Already implemented as `gatherCost` (`cost_funcs.go:552`), which has had no
search caller since it was written. This slice is its first one. `cost_gather`
has **no `enable_*` flag** upstream, so `DisabledNodes` passes the subpath's
count through unchanged.

### 3.3 `cost_gather_merge` — `costsize.c:485`

```
N              = num_workers + 1            /* the leader counts as a worker */
logN           = LOG2(N)
comparison_cost= 2.0 * cpu_operator_cost
startup       += comparison_cost * N * logN            /* heap creation */
run           += rows * comparison_cost * logN         /* heap maintenance */
run           += cpu_operator_cost * rows              /* heap management */
startup       += parallel_setup_cost
run           += parallel_tuple_cost * rows * 1.05     /* +5%: GM blocks on every worker */
disabled_nodes = input_disabled_nodes + (enable_gathermerge ? 0 : 1)
total          = startup + run + input_total_cost
startup_total  = startup + input_startup_cost
```

New: `gatherMergeCost(cp, sub Cost, workers int, outputRows float64) Cost`.

`enable_gathermerge` reaches it as a new `costParams.enableGatherMerge` /
`PlannerSettings.EnableGatherMerge` (default true), which is what
`ParallelSettings.DisableGatherMerge`'s own comment asked for:

> "With no GatherMerge path in the search (P5-04 open) there is no
> disabled_nodes count to carry … Convert to counting when P5-04 lands real
> Gather/GatherMerge paths."

The post-pass keeps its boolean; the path model counts, as PG 18 does.
NOTE (pre-existing, not this slice's): neither field is filled from the session
GUC — `plannerSettingsFrom` (dispatch.go) reads none of the parallel block, so
all five parallel `PlannerSettings` fields sit at their defaults, exactly as
C-19a/b/c left them. Wiring them is P2-02's remainder / C-19h.

---

## 4. Where the paths are generated

### 4.1 `generate_useful_gather_paths` — `allpaths.c:3236`

Ported as `(*searchCtx).generateUsefulGatherPaths(rel *RelOptInfo)`:

1. no partial paths ⇒ nothing to do (upstream's first line);
2. one **Gather** over `PartialPathlist[0]` — the cheapest partial path, which
   is what `addToPartialPathlist`'s ascending-total-cost insertion guarantees
   ("the output of Gather is always unsorted, so there's only one partial path
   of interest", allpaths.c:3116);
3. one **Gather Merge** per partial path that already carries pathkeys
   (`generate_gather_paths`, allpaths.c:3131-3143).

The incremental-sort / explicit-sort half of `generate_useful_gather_paths`
(allpaths.c:3255-3341 — sort a partial path to reach a *useful* ordering, then
Gather Merge it) is **out of scope**: it is C-19e's ("re-decide Gather Merge →
Sort → Parallel scan by cost") and needs `create_sort_path` over a partial path
plus `get_useful_pathkeys_for_relation`. So goopg's function is
`generate_gather_paths` in body and `generate_useful_gather_paths` in name and
call sites, and the missing half is stated here rather than silently absent.

### 4.2 Call sites — PG's, exactly

| PG | goopg |
|---|---|
| `standard_join_search`, per joinrel, immediately before `set_cheapest` (allpaths.c:3503-3517) | `joinSearch`'s per-level loop (`joinsearchlevel.go:295-303`) |
| `merge_clump`, before `set_cheapest` (geqo_eval.c) | the three `setCheapest(joinrel)` sites in `geqo.go` |
| `set_rel_pathlist`, per baserel, **only when `bms_membership(all_baserels) != BMS_SINGLETON`** (allpaths.c) | after `addBaseRelIndexPaths` in `relfromjoinlist.go` |

The single-baserel exclusion is FREE here and worth stating: `makeRelFromJoinlist`
returns a one-item joinlist's rel directly (`relfromjoinlist.go:335`, upstream's
"single joinlist node, so we're done"), so the search is never entered for a
one-relation statement at all. PG excludes that case so the upper planner can
choose partial aggregation instead (`create_grouping_paths`); goopg's structure
excludes it for a different reason and reaches the same place — which is what
keeps TPC-H Q1 (single rel, `Finalize Agg → Gather → Partial Agg → Parallel Seq
Scan`, built by the post-pass's `splitAggregate`) out of this slice's blast
radius entirely.

### 4.3 What is refused, and why (fail-closed, C-19a's discipline)

A path that reaches `createPlan` must be executable. Three refusals, each
naming the wrong answer it prevents:

1. **Zero-worker subpath ⇒ no Gather.** Upstream builds a `single_copy` Gather
   for `num_workers == 0`. goopg's producers never offer a 0-worker partial path
   (`addBaseRelPartialPaths` and `addPartialIndexPath` both `continue` on
   `workers <= 0`), and the executor's `Gather.SingleCopy` is documented as
   "Reserved; nothing sets it yet". Building one would run the child in exactly
   one worker with a leader that also runs it — the shape nothing has tested.
2. **Gather Merge only over a subpath whose driving scan is a SEQ scan.**
   `gatherMergeOp` (`internal/executor/operators_gather_merge.go:186,248`)
   attaches only `attachParallelScan` — NOT `attachParallelIndexScan`, NOT
   `attachParallelBitmapScan`. A Gather Merge over a partial *index* path would
   therefore have every worker read the whole index and return **N copies of
   every row**. This is the same executor gap `sortPartialRootPays` already
   records for the post-pass (parallel.go, C-19c note). Consequence: with today's
   producers (the only pathkey-carrying partial path is the ordered *index*
   twin), **no Gather Merge path is generated in production**; the producer and
   its cost function exist, are unit-tested, and go live when the executor
   attaches the index/bitmap claim sets or when a partial Sort path lands (C-19e).
   Reported to the owner of `operators_gather*.go`.
3. **`createPlan` panics if the built child has no driving scan.**
   `runWorker` (`operators_gather.go:396`) IGNORES `attachParallelScan`'s return
   value: an unattached subtree does not "stay serial", it makes every worker
   read the whole relation. `drivingScan(child) != nil` is the planner-side
   mirror of that walk, and it is asserted at plan-build time — a producer bug,
   which `createplan.go`'s contract says panics rather than silently mis-building.

---

## 5. Admission mode `GOOPG_GATHER_PATHS` — and why the default is `off`

`off` (default) | `top` (final rel only) | `all` (every rel, PG-faithful).
Registered in the flag-provenance table (`flaglabels.go`) so every benchmark
artefact names the arm it measured.

**The default is a MEASURED decision, and the measurement has not been run.**
The honest statement of the risk:

- Today `PartialPathlist` is populated on BASE rels only. A Gather over a base
  rel therefore puts the Gather *below* every join, and the joins run serially
  in the leader. The post-pass, by contrast, puts one Gather ABOVE the whole
  hash-join subtree, so the joins run in the workers. Whenever a base-rel Gather
  wins `add_path` for its rel (it often will — it is the serial scan with its
  CPU divided, and `add_path` compares total cost), the plan would trade a
  whole-tree Gather for a scan-only one AND, by §6's coexistence rule, the
  post-pass would then decline. That is a regression, not a win. It is the
  "ordering trap" take2 07 §3.2 measured from the other direction.
- PG does not have this problem because partial paths PROPAGATE upward:
  `try_partial_hashjoin_path` / `try_partial_mergejoin_path` give a JOINREL its
  own partial paths, so the Gather floats to the top. Producing those is
  **C-19f** (parallel hash) and is explicitly out of this slice.
- With `top`, the Gather is offered only at the search's final rel — the exact
  node the post-pass targets. Today that rel has no partial paths (it is a
  joinrel), so `top` is inert-by-construction on any multi-rel statement, and it
  goes live the moment C-19f populates a joinrel's partial list. It is the mode
  a later agent should measure FIRST.

So this slice lands the mechanism complete and priced, with the switch off, and
records what has to be measured before it moves:

- **A/B on TPC-H SF=1 (port 65433, owned by another agent right now)**: `off`
  vs `top` vs `all`, pinned-stats regime, fresh capped server per arm, values
  24/24 + `make plan-gate` + timing on every query whose plan moved. A row-count
  gate alone cannot catch this class (ledger: "Row-count gate cannot catch a
  plan-shape regression" — 21/21 identical while Q2 went 43× slower), so the
  gate for this flip is TIMING, per query, on the moved plans.
- **TPC-DS SF0.5 sweep** for the values arm.

Until then the switch is a measurement instrument, which is the same shape
`GOOPG_INDEX_PROBE_MULT` had before its calibration was run — and that knob's
history (shipped at the value its own comment called wrong; 1.0→2.0 was worth
27% of TPC-H) is the reason the default is written here as an open question
rather than guessed.

---

## 6. Coexistence with `MaybeAddGather` (C-19h is a SEPARATE item)

Rule, implemented in `MaybeAddGather` itself: **if the tree already carries a
`*Gather` or `*GatherMerge` anywhere, the post-pass returns the root unchanged.**

This is not merely tidiness. `findPartialSubtree` treats `*Gather` as a node
that "terminates partial-ness" and then DESCENDS through it
(`terminatesPartial` → single child → `cur = kids[0]`), so without this rule the
post-pass would nest a second Gather *below* the path model's one: N workers
each launching N workers, and `prebuildHashJoins`'s "a Gather never appears
inside another Gather's partial subtree" comment silently falsified. The rule is
a whole-tree scan for the two node types, run once at entry, before the
`Explain` unwrap descends.

Direction of the precedence is deliberate: the PATH MODEL WINS. A plan that
carries a costed Gather has had parallelism decided by `add_path`; re-deciding
it with a size rule is precisely the defect Phase 5 removes. With the mode at
`off` the rule never fires in production today, and it is the one line C-19h
deletes (together with the pass).

---

## 7. `createPlanNode` arms

Both in a new `createplangather.go`, following `createSortPlan`'s shape (the
existing single-child wrapper arm):

```
case PathGather:       return createGatherPlan(p)        // child layout passes through
case PathGatherMerge:  return createGatherMergePlan(p)
```

- exactly one child, non-nil, with `ParallelWorkers > 0` — else panic (producer bug);
- recurse with `createPlanNode` to get `(childNode, layout)`;
- `stampParallelScan(childNode)` — the SAME copy-on-write traversal the post-pass
  uses, so a path-model Gather and a post-pass Gather label the identical scan;
- `drivingScan(stamped) != nil` asserted (§4.3 item 3);
- wrap in `NewGather(pos, stamped, workers)` / `NewGatherMerge(pos, stamped,
  workers, keys)` where `keys` is `PathKey → SortKey` with `createSortPlan`'s
  direction negation (`SortAsc` is ascending-true, `SortKey.Desc` is
  descending-true);
- return the CHILD's `outputLayout` unchanged: a Gather reorders rows, never
  columns — the same statement `createSortPlan` makes.

`Gather` / `GatherMerge` gain an embedded `PlanCost` so `stampPlanCost` (the
single funnel in `createPlanNode`) carries the path's cost onto the node and
EXPLAIN prints the searched cost instead of `DeriveLegacyDisplayCost`'s
monotone stand-in. Post-pass-built Gathers leave `CostSet` false and keep the
legacy display, exactly as today.

---

## 8. How we will know it worked

**In this slice (unit, no server):**

1. `gatherMergeCost` reproduces `cost_gather_merge` term by term, expressed
   through the named `costParams` fields — never a literal (a test that pins a
   literal pins a stale calibration; that mistake is what hid the index-probe
   multiplier).
2. A Gather path WINS over the serial scan exactly when
   `serial.Total - partial.Total > parallelSetupCost + parallelTupleCost*rows`,
   and LOSES when the relation is small enough that it does not — asserted
   through the constants, both directions, so the crossover is the thing pinned.
3. `Rows` round-trips: `computeGatherRows(partial) == clamp(rel.Rows)` for a
   partial path produced by `addPartialSeqScanPath`, i.e. the divisor applied by
   the scan and undone by the Gather is one divisor, not two.
4. Gather Merge is more expensive than Gather over the same subpath (heap +5%),
   and `enable_gathermerge = off` adds exactly one to `DisabledNodes`.
5. `createPlanNode` emits `*Gather` with `WorkersPlanned == subpath workers` and
   a `Parallel`-stamped driving scan; a Gather path over a subtree with no
   driving scan panics.
6. `MaybeAddGather` returns its input unchanged on a tree that already carries a
   Gather — the no-double-stamp pin.
7. Mode `off` produces no `PathGather` at all: the search is unchanged
   by construction, which is this slice's serial-control-arm argument.

**Beyond this slice (needs the bench cluster, currently owned elsewhere):**
the acceptance argument for C-19d is the D-05 re-run. Re-apply
`tmp/d05p{2,3,4}-*.patch` on top of C-19d+C-19f with the mode on and the guard
`TestSlice3LiveQ9ShapeDerivation` green: each correction must now cost ≲0 on the
suite, because a plan that leaves the hash-join shape can carry its Gather with
it. If Q5/Q9/Q10 still collapse, C-19d has not achieved its purpose and the
remaining gap is upward propagation (C-19f/g), not the price of a Gather. That
re-run cannot be done in this slice: it needs partial JOIN paths to exist, and
it needs the TPC-H cluster.
