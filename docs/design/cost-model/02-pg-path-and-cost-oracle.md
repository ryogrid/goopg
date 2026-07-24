# 02 — PostgreSQL's Path and Cost Model as the Oracle

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-22 |
| depends on | [01](01-current-state-and-gap-analysis.md) |
| baseline | PostgreSQL 18.3 (`postgres/` submodule) |

## 0. Why this chapter exists

goopg's project premise is byte-faithful reproduction of PostgreSQL. For the cost
model that means the cost *functions* and *constants* are copied from PG source,
not invented, and every later chapter cites back here. This chapter is the
reference: the objects (`RelOptInfo`, `Path`), the selection algorithm
(`add_path` / `set_cheapest`), the per-node cost functions, the constants, and the
`Path → Plan` translation. It reproduces formulas at the level of detail an
implementer needs and no further; the source is the authority and is cited
inline.

## 1. The objects

### 1.1 `RelOptInfo` — a relation and its candidate paths

Every base relation and every join relation PG considers is a `RelOptInfo`
(`postgres/src/include/nodes/pathnodes.h`). The fields this bundle reproduces:

- `rows` — the estimated row count of the relation, computed **once** by
  `set_baserel_size_estimates` (`costsize.c:5349`) for base rels and by
  `set_joinrel_size_estimates` for joins. Every path over the rel shares it. This
  is the single-source-of-truth for cardinality — invariant #2 of the
  [README](README.md).
- `pathlist` — the surviving candidate `Path`s (serial).
- `partial_pathlist` — the surviving candidate *partial* paths (each executed by
  one parallel worker over its share of the data); the seed for parallelism.
- `cheapest_total_path`, `cheapest_startup_path` — set by `set_cheapest`
  (`pathnode.c:272`).
- `reltarget->width` — the estimated average tuple width in bytes, the input to
  every per-tuple and sort cost.

### 1.2 `Path` — one way to produce a relation, with a cost

A `Path` (`pathnodes.h`) carries:

- `startup_cost` — cost expended before the first row can be returned.
- `total_cost` — cost to return all rows.
- `rows` — the path's own row estimate (usually the rel's `rows`, but a parameter-
  ised or partial path differs).
- `pathkeys` — the ordering the path guarantees ([04](04-pathkeys-and-ordering.md)).
- `parallel_safe`, `parallel_workers` — parallel eligibility and degree.

The **two-number cost** is the crux. A cheaper-total path can lose to a
cheaper-startup one under a `LIMIT`; PG keeps both frontiers, which is why
`add_path` compares on both axes.

## 2. The selection algorithm

### 2.1 `add_path` — dominance pruning with a fuzz factor

`add_path` (`pathnode.c:464`) is called for every candidate. It keeps `new_path`
only if no existing path **dominates** it and removes any existing path
`new_path` dominates. Dominance is decided by `compare_path_costs_fuzzily`
(`pathnode.c:185`) across **five** dimensions, and the first one is not a cost at
all: **`disabled_nodes`** — the count of disabled plan nodes below the path —
*"trumps all else"* and is compared before any cost term (`pathnode.c:191`). Then
come total cost, startup cost, pathkeys (a path with strictly more useful ordering
is not dominated by a cheaper unordered one), and `parallel_safe`. The pathlist is
kept sorted by `disabled_nodes` then `total_cost` (`pathnode.c:436`). Costs are
compared with a multiplicative tolerance:

```c
#define STD_FUZZ_FACTOR 1.01      /* pathnode.c:50 */
```

so a path within 1 % is treated as cost-equal and the tie is broken on the other
dimensions. **This fuzz factor is the reproducibility mechanism** an integer→float
cost migration needs ([07](07-cost-driven-join-order.md) §4): without it, floating
noise flips plan choice between runs and destroys plan-gate determinism.

### 2.2 `set_cheapest` and disabled nodes

After all paths are added, `set_cheapest` (`pathnode.c:272`) selects the
`cheapest_total_path` and `cheapest_startup_path` for the rel. Disabled node types
(`enable_hashjoin = off`, etc.) are not removed from consideration. In PostgreSQL
18 the mechanism is **not** an additive `disable_cost` any more — that was the
pre-v17 design. Instead, disabling a node type increments the path's
`disabled_nodes` counter (`costsize.c:357`, `:430`, `:469`), and because
`compare_path_costs_fuzzily` compares `disabled_nodes` first (§2.1), a path with
fewer disabled nodes wins regardless of cost — so a discouraged plan is chosen only
when nothing else can produce the result. The old constant `disable_cost = 1.0e10`
still exists (`costsize.c:141`) but only for residual internal uses (e.g. hash
aggregation forced over `work_mem`, `costsize.c:4421`), not as the `enable_*`
mechanism. This bundle treats `disabled_nodes` / `enable_*` as a **documented
divergence** initially ([03](03-path-substrate-and-plan-creation.md) §6): goopg has
no `enable_*` GUCs today, so its `disabled_nodes` is trivially always 0, and the
milestone does not need them.

## 3. The cost constants (already present in goopg)

PG's cost unit is defined by `seq_page_cost = 1.0`: every other constant is
relative to reading one sequential page. From `postgres/src/include/optimizer/cost.h:24-30`:

| constant | value | meaning |
| --- | ---: | --- |
| `seq_page_cost` | 1.0 | read one page sequentially (the unit) |
| `random_page_cost` | 4.0 | read one page at random |
| `cpu_tuple_cost` | 0.01 | process one tuple through a plan node |
| `cpu_index_tuple_cost` | 0.005 | process one index entry |
| `cpu_operator_cost` | 0.0025 | evaluate one operator/function |
| `parallel_tuple_cost` | 0.1 | pass one tuple from a worker to the leader |
| `parallel_setup_cost` | 1000.0 | start parallel workers (a flat per-Gather addend) |

**goopg already registers all seven**, with these exact boot values, in
`internal/config/defaults.go` (`seq_page_cost` `:859`, `cpu_tuple_cost` `:870`,
`cpu_index_tuple_cost` `:877`, `cpu_operator_cost` `:884`, `random_page_cost`
`:688`, `parallel_setup_cost` `:726`, `parallel_tuple_cost` `:734`). They exist
because GUC defaults must match PG ([memory: GUC defaults must match PG]) — but
**nothing reads them**; they are `SHOW`-only today. The cost model's job is to
make the planner consume the constants it already advertises. `BLCKSZ` is 8192.

## 4. The per-node cost functions

Reproduced to the depth an implementer needs; `costsize.c` line numbers are the
authority.

### 4.1 Sequential scan — `cost_seqscan` (`costsize.c:295`)

```
startup = 0                                   (plus any qual startup)
run     = seq_page_cost · relpages
        + (cpu_tuple_cost + cpu_operator_cost·qpqual_width) · reltuples
total   = startup + run
```

Under parallelism the run cost's tuple and page terms are divided by
`get_parallel_divisor(path)` (§4.8); `cost_seqscan` takes the parallel worker
count on the path and does this internally.

### 4.2 Index scan — `cost_index` (`costsize.c:560`)

Combines the index-access cost (a function of `random_page_cost`, the index
selectivity, and correlation) with the heap-fetch cost. The correlation term
interpolates between `seq_page_cost` (perfectly correlated → sequential heap
access) and `random_page_cost` (uncorrelated → random). goopg's index scan
materialises the whole TID list eagerly
(`internal/executor/operators_index.go`), a divergence noted where it costs
([06](06-scan-and-join-path-costs.md) §2.2).

### 4.3 Sort — `cost_sort` (`costsize.c:2144`)

For an in-memory sort of `N` tuples of width `W`:

```
startup ≈ comparison_cost · N · log2(N)      (comparison_cost = 2·cpu_operator_cost)
        + input startup
run     = cpu_operator_cost · N
```

When `N·W` exceeds `work_mem`, PG switches to the external-merge model
(`seq_page_cost` per page over `2·⌈logM(runs)⌉` merge passes). Sort cost is
**startup-heavy** — nearly all of it is paid before the first row — which is why
the two-number cost matters: a sort under a `LIMIT` is expensive at startup but
its total is what a merge join pays. `W` here is the tuple width, which forces the
width estimator into existence ([05](05-statistics-and-estimation-inputs.md) §2).

### 4.4 Aggregate — `cost_agg` (`costsize.c:2682`)

`cost_agg` has **no `aggsplit` parameter**: the partial/final distinction is
carried entirely by the `AggClauseCosts` the caller supplies (a COMBINE split
charges the combine function instead of the transition function). For the hashed
arm it charges, per **input** tuple, `transCost.per_tuple + cpu_operator_cost ·
numGroupCols`, and per **output group**, `finalCost.per_tuple + cpu_tuple_cost`.
This is the formula [parallel-query/11](../parallel-query/11-partial-aggregation-cost-model.md) §1.3
already transcribed; [08](08-parallel-paths-and-degree.md) §5 reconciles it with
goopg's mutex-merge model.

### 4.5 Hash join — `initial_cost_hashjoin` / `final_cost_hashjoin` (`costsize.c:4160` / `:4275`)

Costed in two stages so `add_path` can prune early. `initial_cost_hashjoin`
charges the build: read the inner side and hash it — roughly `(cpu_operator_cost ·
num_hashclauses + cpu_tuple_cost) · inner_rows` plus the inner subpath's total,
all in **startup** (the hash table must be complete before probing). Batching adds
page I/O when the inner exceeds `work_mem`. `final_cost_hashjoin` adds the probe:
per outer row, hash the key and walk the matching bucket. The asymmetry — build is
startup, probe is run — is why the build side should be the *smaller* one, which
this cost derives rather than hard-codes ([06](06-scan-and-join-path-costs.md) §3).

### 4.6 Nested loop — `final_cost_nestloop` (`costsize.c:3349`)

`outer_rows · (inner_rescan_cost) + outer_path_total`. For an inner index scan the
rescan cost is one parameterised index probe, which is what makes a nested-loop-
index join cheap for a selective outer side. `compute_semi_anti_join_factors`
(`costsize.c:5114`) supplies the `match_frac` that lets a semi join stop at the
first inner match — the oracle for goopg's NLI semi/anti gate
([correlated-subquery-planning/06](../correlated-subquery-planning/06-cost-model-touchpoints.md) §3.2,
[06](06-scan-and-join-path-costs.md) §3.3).

### 4.7 Merge join — `final_cost_mergejoin` (`costsize.c:3837`)

Cost of merging two sorted inputs, plus a `cost_sort` on either side that is not
already sorted (a path whose `pathkeys` satisfy the merge clause pays no sort —
this is the entire reason pathkeys exist, [04](04-pathkeys-and-ordering.md)).

### 4.8 Gather and the parallel divisor — `cost_gather` (`costsize.c:446`), `get_parallel_divisor` (`costsize.c:6474`)

```
cost_gather:  startup += parallel_setup_cost           (flat, 1000)
              run     += parallel_tuple_cost · output_rows
```

The Gather adds a flat setup and a per-tuple transfer over the rows *emerging*
from it. The rows emerging are reconstructed by `compute_gather_rows`
(`costsize.c:6625`) as `partial_path_rows · parallel_divisor`. The **divisor**
(`get_parallel_divisor`, `costsize.c:6474`) adds the leader's fractional
contribution only when positive:

```c
parallel_divisor = path->parallel_workers;
leader_contribution = 1.0 - (0.3 * path->parallel_workers);
if (leader_contribution > 0)
    parallel_divisor += leader_contribution;
```

so `d(1) = 1.7`, `d(2) = 2.4`, `d(3) = 3.1`, `d(w ≥ 4) = w`. **The load-bearing
point for [08](08-parallel-paths-and-degree.md):** the partial path *underneath*
the Gather already has its scan, filter, and join costs divided by this divisor
(each worker touches ~1/d of the pages). So the Gather does not make a plan more
expensive by fiat — it divides the bulk of the plan's cost by `d` and adds back
`parallel_setup_cost + parallel_tuple_cost · rows`. Whether that trade wins is the
parallelize decision, and it is a genuine cost comparison only because the partial
path is a first-class object with its own divided cost. `cost_gather_merge`
(`costsize.c:485`) is the ordered variant, adding a heap-merge term over the
worker streams.

### 4.9 Worker count is not a cost decision — `compute_parallel_worker` (`allpaths.c:4274`)

Critically: PG does **not** search for the best worker count. `compute_parallel_worker`
is a pure size ladder — start at 1 worker, add one per factor-of-three increase in
heap pages, cap at `max_parallel_workers_per_gather`. PG picks the count by size,
then costs that *single* Gather candidate to decide keep-or-drop. goopg already
mirrors the ladder faithfully in `computeParallelWorkers`
(`internal/planner/parallel.go:459`). Reproducing the oracle means **keeping the
ladder for the count** and making only *whether to Gather* the cost decision —
invariant #3 of the [README](README.md).

## 5. `Path → Plan`: `create_plan` (`createplan.c:337`)

Selecting the cheapest path does not produce something the executor runs. PG's
`create_plan` (`createplan.c:337`) walks the winning path top-down
(`create_plan_recurse`, `:388`) and emits a `Plan` node per path node — the switch
dispatches `T_SeqScan` (`:397`), `T_IndexScan` (`:399`), `T_HashJoin` (`:415`),
`T_MergeJoin` (`:416`), `T_NestLoop` (`:417`), `T_Gather` (`:484`), `T_Sort`
(`:488`), `T_Agg` (`:502`), `T_GatherMerge` (`:540`). Crucially, `create_plan`
also **inserts the Sort nodes** the chosen pathkeys imply but that no explicit
sort path created (e.g. the inner sort a merge join needs). goopg's analogue
translates to its *existing* executor Node types rather than a new `Plan` IR
([03](03-path-substrate-and-plan-creation.md) §3) — the one place goopg's Path
model deliberately stops short of PG's.

## 6. Pathkeys, in one paragraph

A pathkey (`pathkeys.c`) canonicalises "this path is ordered by expression E,
direction D, under equivalence class C". `pathkeys_contained_in`
(`pathkeys.c:343`) tests whether one ordering satisfies another (a merge join's
required order, an `ORDER BY`, a Gather Merge's merge key).
`make_pathkeys_for_sortclauses` (`pathkeys.c:1336`) builds them from an `ORDER BY`;
`build_index_pathkeys` (`pathkeys.c:740`) from an index's sort order. The full
machinery is equivalence-class-driven and large; [04](04-pathkeys-and-ordering.md)
scopes goopg to the minimum the milestone needs and defers the rest explicitly.

## 7. Divergence from PostgreSQL

- **`create_plan` targets goopg's executor Nodes, not a new `Plan` IR** (§5,
  [03](03-path-substrate-and-plan-creation.md) §3). goopg's executor is not being
  rewritten.
- **`disabled_nodes` / `enable_*` are deferred** (§2.2). No `enable_hashjoin`-style
  GUCs exist in goopg, so its `disabled_nodes` is always 0 and the first dominance
  dimension is a no-op; the milestone does not need them. Recorded so a reviewer
  does not read the omission as an oversight, and so the reproduced comparator
  ([03](03-path-substrate-and-plan-creation.md) §2) is understood to carry the
  dimension even though it never fires.
- **The constants are PG's, verbatim** (§3) — goopg already ships them. Any future
  retuning is a divergence that must be argued, not drifted into ([memory: GUC
  defaults must match PG]).
- **Extended-statistics and GEQO paths are out of scope** — the independence
  assumption in size estimation stands unaided, and the DP handles goopg's
  ≤12-relation joins without a genetic fallback.
