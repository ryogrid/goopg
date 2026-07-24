# 08 — Planner Integration, GUCs, and EXPLAIN

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-21 |
| depends on | [02](02-pg-target-architecture.md) §4, [06](06-parallel-aggregation.md) §5 |

This chapter answers three questions: where a Gather goes, how many workers it
gets, and how the decision reaches the planner at all — plus the EXPLAIN
surface that makes any of it observable.

## 1. Where the Gather goes

The planner runs its existing pipeline unchanged and then applies a **post-pass**
that considers inserting a Gather. This mirrors the shape of the existing NLI
and Memoize rewrites (`rewriteJoinsToNLI`, `maybeAttachMemoize`) rather than
introducing PG's partial-path machinery into `internal/planner/bushy.go`, which
has no path abstraction to extend.

Insertion is considered at exactly two shapes in v1:

```
(a)  Aggregate                    (b)  <serial parent>
       -> …                              -> Gather
     becomes                                  -> <partial subtree>
     Finalize Aggregate
       -> Gather / Gather Merge
            -> Partial Aggregate
                 -> <partial subtree>
```

A subtree is **partial-capable** when its leaf driving scan is a `SeqScan`
eligible under §2 and every node between that scan and the Gather is
row-wise — `Filter`, `Project`, `Sort` (for Gather Merge), and the probe side
of a `Join` whose build side is serial ([07](07-parallel-hash-join.md) §3.1).

Nodes that terminate partial-ness upward, i.e. the Gather must be at or below
them: `Limit`, `Distinct`/`DistinctOn`, `WindowAgg`, `SetOp`,
`RecursiveUnion`/`WorkTableScan`, `NestedLoopIndexJoin` (its inner is an index
probe bound to the outer row — out of scope with parallel index scan out of
scope), `Memoize`, and **the inner side of any correlated SubPlan or LATERAL
join** (a Gather there would need param slots rebound per outer row, which the
fan-out snapshot in [03](03-concurrency-substrate.md) §2.2 cannot provide).

### 1.1 Whole-plan refusals

Independent of shape, the post-pass refuses to produce any Gather when the
statement:

- **is DML**, or the transaction is **SERIALIZABLE** ([README](README.md));
- **carries row marks** — `SELECT … FOR UPDATE / FOR SHARE`. This is *not*
  covered by the DML refusal: a locking SELECT is a read statement that
  nonetheless stamps xmax and needs `LockMgr`, and workers may not release
  locks ([03](03-concurrency-substrate.md) §6.2). PG likewise disables
  parallelism outright for plans with rowMarks. Note goopg has dedicated
  machinery here (`preserveCTIDRel` → `buildHashRightWithCTID`,
  `internal/executor/operators_join_agg.go:601-604,614`), which is exactly the
  code path a parallel plan must not reach;
- **touches a temporary table**. PG marks temp access parallel-restricted.
  goopg's shared address space might well make it safe — `TempTableShadows`
  (`internal/executor/context.go:368`) is per-session state, not per-process —
  but "might well be safe" is not an argument, and no chapter here establishes
  one. Refused in v1, with the note that lifting it is a genuine Go-native
  opportunity someone should analyse rather than assume;
- **scans a virtual catalog relation.** The `Pg*Rows func() [][]string`
  callbacks (`context.go:536-655`) are the row source for virtual catalog
  scans, and [03](03-concurrency-substrate.md) §2.5 nils exactly those in a
  worker context. A Gather over `pg_class` would nil-panic at the call site.
  That is §2.5 working as intended, but the planner must not build the plan in
  the first place;
- **contains a parallel-unsafe function** (§5).

Placement rule: **push the Gather as low as possible while keeping the partial
subtree as large as possible** — i.e. immediately below the lowest
partial-terminating node. That is PG's outcome too, arrived at there by
costing partial paths and here by construction.

## 2. Worker count

Reproduced from PG's `compute_parallel_worker()`
(`postgres/src/backend/optimizer/path/allpaths.c:4273`), which is a *size rule*,
not a cost comparison — which is exactly why it is reproducible here despite
[01](01-current-state-and-gap-analysis.md) §4.

```
if table has a parallel_workers reloption:
        workers = that value
else:
        if heap_pages < min_parallel_table_scan_size:   return 0   // no Gather
        workers   = 1
        threshold = max(min_parallel_table_scan_size, 1)
        while heap_pages >= threshold * 3:
                workers++
                threshold *= 3
workers = min(workers, max_parallel_workers_per_gather)
```

With PG's real default (1024 blocks = 8 MB) the ladder is ≥8 MB → 1 worker,
≥24 MB → 2, ≥72 MB → 3, ≥216 MB → 4.

Three details of PG's function the sketch above elides, all of which the
implementation should carry:

- an **independent index ladder** with the final count being
  `Min(heap_workers, index_workers)` (`allpaths.c:4324-4341`) — inert while
  parallel index scan is out of scope, but the structure should not be designed
  away;
- the **`RELOPT_BASEREL` gate** (`allpaths.c:4293-4296`): the below-threshold
  early return applies only to base relations, so an inheritance child still
  gets a partial path even when it is individually too small, because its
  siblings may make the total worthwhile;
- the **`INT_MAX/3` overflow break** in the ladder loop (`allpaths.c:4317`).

Note also that PG's final clamp is against the caller-supplied `max_workers`
argument, not against the GUC read inside the function; `max_parallel_workers_per_gather`
is applied by the caller.

Inputs available today:

- **`heap_pages`** — the relation's block count, already read at scan Open via
  `smgr` `NBlocks` and cached (`internal/executor/operators_storage.go:691`).
  The planner needs it at plan time; `catalog.Table` carries stats
  (`Stats.RowCount`) but block count must come from the storage manager. If
  that plumbing proves awkward, estimated rows × average width is an acceptable
  approximation — but it is an approximation, and choosing it must be recorded,
  because it changes which relations cross the threshold.
- **`parallel_workers` reloption** — already parsed and stored
  (`internal/executor/operators_ddl.go:2125-2145`), currently unread. Honouring
  it is nearly free and is required for PG fidelity.

### 2.1 Additional gates beyond PG's rule

PG decides *whether* to use the partial path by comparing costs. goopg has no
cost currency ([01](01-current-state-and-gap-analysis.md) §4), so the size rule
above decides both count and eligibility, plus these stats-gated refusals in the
established style of `nliCostGateAccepts` / `innerUnwrapCostAccepts`:

- **No statistics → no Gather.** Consistent with `EstimateRows` returning 0 for
  "no estimate" and callers being documented to skip cost decisions on 0
  (`internal/planner/cardinality.go:32-34`).

  This is the **opposite** default from the semi/anti NLI gate, which
  optimistically accepts without stats. The asymmetry is deliberate and is the
  same reasoning the D6.3b INNER-unwrap gate used: accepting without evidence
  risks the bad direction (spawning workers for a tiny relation, plus N× the
  memory of §4.2), while declining merely keeps today's behaviour. goopg's
  ANALYZE statistics are in-memory and lost on restart, so "no stats" is a
  *common* production state, not an edge case — which is precisely why the
  choice of default matters and must be argued rather than assumed.
- **High-cardinality grouping caution.** There is no hash-agg spill
  ([06](06-parallel-aggregation.md) §4.2), so N workers each holding a full
  group table is a memory risk serial execution does not have. Estimated
  distinct group count is an input to the gate.

### 2.2 A kill switch, in the house style

`GOOPG_PARALLEL=off` as a process-global `atomic.Bool` read in `init()`,
matching `memoizeOn` (`internal/planner/memoize.go:31-42`) and the NLI cost-gate
legacy switch (`nl_index_join.go:1204-1208`). Every prior planner feature in
this codebase shipped with one and every one of them earned its keep.

## 3. On `parallel_setup_cost` and `parallel_tuple_cost`

Both GUCs stay registered at PG's defaults (1000 and 0.1) for compatibility —
`SHOW` and `pg_settings` must keep answering — but **neither is consulted** in
v1, because there is no total-cost quantity to add them to.

This is worth stating plainly rather than hiding in a table, because the
constants also do not *describe* goopg. `parallel_setup_cost = 1000` encodes
process fork plus DSM creation plus state restoration; a goroutine costs
microseconds. If goopg later grows a cost model, the right value for this
constant is far smaller than PG's, and the threshold at which parallelism pays
is correspondingly lower. Adopting PG's number unexamined at that point would
suppress parallel plans that are in fact profitable.

For now the size rule alone governs, which has the honest property of being a
rule this engine can actually evaluate.

**Superseded in part by [11](11-partial-aggregation-cost-model.md).** The
partial-aggregation split needs a comparison the size rule cannot express, so
chapter 11 supplies one — self-contained, comparing only split against
no-split, which share their entire subtree and therefore need no absolute
scale. It also sets the goopg values this section argued for rather than
assumed: `parallel_setup_cost` at 50 against PG's 100 000 (relative to
`cpu_tuple_cost`), and `parallel_tuple_cost` at 2 against PG's 10. Both remain
un-consulted for Gather PLACEMENT, which is still governed by the size rule
alone; chapter 11 consults them only for the split decision.

## 4. GUC plumbing

### 4.1 The problem

`planner.Plan(stmt, cat)` (`internal/planner/planner.go:89`) takes no settings,
and the existing GUC→planner bridge (`Registry.OnChange`,
`internal/config/guc.go:423`, wired at `cmd/goopg/main.go:397,403`) sets a
**process-global** `atomic.Bool`. That is fine for a boolean kill switch where
last-writer-wins is acceptable. It is wrong for
`max_parallel_workers_per_gather`, a per-session integer: two sessions with
different settings would fight.

### 4.2 The solution — an existing precedent

The codebase already threads per-session integer GUCs into execution:

```go
// internal/server/dispatch.go:1087
func sessionStatsTarget(sess *config.SessionRegistry) int { … }

// internal/server/dispatch.go:376, dispatch_extended.go:155
ectx.StatsTarget = sessionStatsTarget(sess)
```

with `Context.StatsTarget` documented as "the effective
`default_statistics_target`" (`internal/executor/context.go:190-197`).

The parallel GUCs follow this pattern exactly: `sessionMaxParallelWorkers`,
`sessionMinParallelTableScanSize`, `sessionParallelLeaderParticipation`,
`sessionDebugParallelQuery` readers in `dispatch.go`, assigned onto the
executor context at the same two sites, and reaching the planner through
whatever channel the Gather post-pass is invoked on. Because the post-pass runs
after `Plan()` rather than inside it, `Plan`'s signature need not change — the
settings can be passed to the post-pass directly.

### 4.3 GUC corrections required

Independently of parallelism, two registrations are wrong
([01](01-current-state-and-gap-analysis.md) §5.1):

| GUC | current | PG | fix |
| --- | --- | --- | --- |
| `min_parallel_table_scan_size` | `UnitKB`, `8388608` → shows `8GB` | `GUC_UNIT_BLOCKS`, 1024 blocks → shows `8MB` | boot value `8192` with `UnitKB`, or add `UnitBlocks` |
| `min_parallel_index_scan_size` | `UnitKB`, `524288` → shows `512MB` | 64 blocks → shows `512kB` | boot value `512` with `UnitKB`, or `UnitBlocks` |

Both boot values are correct byte counts mislabelled as KB. goopg has no
`UnitBlocks` (`internal/config/guc.go:77`); adding one is the higher-fidelity
fix since PG's unit really is blocks and `SHOW` output would then match for any
value, not just the default.

Three further fidelity gaps surfaced during review and belong in the same fix:

- **`debug_parallel_query` accepts only `off`/`on`/`regress`**
  (`internal/config/defaults.go:724-730`). PG additionally accepts
  `true`/`false`/`yes`/`no`/`1`/`0`
  (`postgres/src/backend/utils/misc/guc_tables.c:395-404`), so
  `SET debug_parallel_query = true` still fails — which is precisely the
  upstream-spec compatibility the goopg registration comment cites as its
  reason for existing. **This one is blocking**: it is the lever
  [09](09-verification-and-measurement.md) §1 builds the primary correctness
  gate on.
- **`parallel_setup_cost` / `parallel_tuple_cost` have `MaxVal: 1e15`**
  (`defaults.go:703-716`); PG's ceiling is `DBL_MAX`
  (`guc_tables.c:3937-3958`).
- **The `min_parallel_*` `MaxVal` is unit-mislabelled too.** `715827882` is
  PG's `INT_MAX/3` expressed in *blocks*, applied here as KB. Fixing only the
  boot value leaves the ceiling wrong by the same factor — the fix must change
  `MaxVal` as well.

And one GUC is missing entirely: **`max_parallel_workers`**, the cluster-wide
cap.
It matters more here than in PG. PG's worker slots are a hard, pre-allocated
resource, so oversubscription is impossible by construction; goroutines are
cheap enough that a handful of concurrent sessions each planning 4 workers can
oversubscribe the machine with nothing to stop them. A process-global
semaphore sized by this GUC, acquired at Gather Open and released at Close, is
the mechanism — and it is what makes `Workers Launched:` differ meaningfully
from `Workers Planned:`.

## 5. Parallel-safety gating

`proparallel` is already parsed by `CREATE FUNCTION` and stored
(`internal/executor/sys_pg_proc.go:122-124`, default `'u'`; semantics
documented at `internal/initdb/pg_proc_view.go:236-238`). Nothing consults it.

The Gather post-pass must walk the candidate partial subtree's expressions and
refuse if any function is:

- `'u'` (unsafe) — no Gather anywhere in the plan;
- `'r'` (restricted) — may appear above the Gather but not below it.

Built-in functions need a parallel-safety classification too. goopg already has
a volatility classifier for subplans (deny-list of volatile builtins plus the
catalog's `Volatile` marker, `internal/executor/subplan.go`), and the same
shape applies: volatile builtins are parallel-unsafe by default, with a
reviewed allow-list of safe ones.

The mechanical backstop is [03](03-concurrency-substrate.md) §2.4 — connection
callbacks are nil in a worker context, so a function that slipped through the
gate and tries to touch session state panics at the call site instead of
silently corrupting it.

## 6. EXPLAIN

Four touch points, all in `internal/executor/operators_explain.go`:

1. **`describePlan`** (`:1059`) — labels: `Gather`, `Gather Merge`,
   `Parallel Seq Scan on %s`, and the `Partial `/`Finalize ` aggregate
   prefixes. Note the goopg-specific `(stats)` suffix on scans
   (`:1109,1112`) composes as `Parallel Seq Scan on lineitem (stats)`
   ([04](04-parallel-scan.md) §6).
2. **`planChildren`** (`:1221`) — **a new node is invisible in EXPLAIN until
   added here.** Easy to forget; the plan-gate diff is what catches it.
3. **`emitNodeDetailLines`** (`:775`) — `Workers Planned: N`, plan-time, so it
   renders in plain EXPLAIN as PG does.
4. **`walkPlanAnalyze` / `walkPlanAnalyzeFiltered`** (`:805`,`:811`) —
   `Workers Launched: N`, execution-time, following the Memoize counter pattern
   verbatim: a `map[*planner.Gather]*GatherStats` threaded as a new parameter
   and emitted beside the Memoize block at `:862-870`, with the map registered
   on the executor context as `MemoizeStats` is (`context.go:181`).

`describePlanVerbose` (`:1031`) needs no change beyond falling through to
`describePlan`.

### 6.1 The `GroupAggregate` label correction

`describePlan` emits `GroupAggregate (%d keys)` (`:1095`) for a runtime that is
hash-based in every case ([06](06-parallel-aggregation.md) §4.1). Adding
`Partial `/`Finalize ` prefixes would cement the misnomer. The label should
become `HashAggregate`, which is a plan-gate-visible change on every grouped
query and is therefore sequenced as its own step with a snapshot recapture in
[10](10-roadmap.md).

## 7. Plan stability expectations

Every stage of this bundle up to the point where a Gather is first *inserted*
should produce **zero TPC-H plan changes** — the machinery exists but no plan
uses it. The two exceptions are deliberate and each gets its own recapture:

- the `HashAggregate` label correction (§6.1), which touches every grouped
  query's EXPLAIN text without changing any runtime behaviour;
- the first stage that enables insertion, which by design changes many plans at
  once and is the point at which the identity gate in
  [09](09-verification-and-measurement.md) does its real work.

## 8. Divergence from PostgreSQL

| PG | goopg | Cost |
| --- | --- | --- |
| Partial paths are first-class in the path system; `generate_gather_paths` competes them by cost | Post-pass rewrite over the finished plan | No path abstraction to extend; matches the existing NLI/Memoize rewrite style. Loses PG's ability to consider partial and serial variants of *every* subpath |
| `parallel_setup_cost` / `parallel_tuple_cost` participate in a real comparison | Registered, unread (§3) | goopg cannot evaluate them; the size rule decides instead |
| `compute_parallel_worker()` size ladder | Reproduced exactly (§2) | None — this part is a rule, not a cost |
| `max_parallel_workers` bounds a pre-allocated process-slot pool | Must be added as a semaphore (§4.3) | goroutines have no natural scarcity, so the cap must be created rather than inherited |
| Per-session GUCs reach the planner through the backend's global state | Threaded through the executor context, following `StatsTarget` (§4.2) | None — the precedent exists |

The recurring theme: PG's *decisions* are reproducible because they are rules;
PG's *costing* is not, because goopg has no cost currency. This bundle
reproduces the rules faithfully and declines to fake the costing.
