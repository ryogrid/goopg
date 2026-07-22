# 03 — The Path Substrate and Plan Creation in goopg

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-22 |
| depends on | [02](02-pg-path-and-cost-oracle.md) |

## 0. Why this chapter exists — and what it must not break

This is the load-bearing chapter. It introduces the objects the rest of the
bundle costs, selects, and translates: `RelOptInfo`, `Path`, `add_path`,
`set_cheapest`, and `create_plan`. It is the analogue of
[parallel-query/03](../parallel-query/03-concurrency-substrate.md) — the chapter
that, if wrong, makes every downstream chapter wrong.

It carries two hard constraints. First, goopg's **executor is not being
rewritten**: the Path layer produces the *existing* `planner.Node` types the
executor already runs. Second, the change must be **plan-preserving until the
cost model is switched on** — introducing paths must not alter a single plan while
the DP still uses its integer cost, so the substrate can land and be gated on
"byte-identical plans to today" before any behaviour changes
([11](11-roadmap.md) C1).

## 1. The Path and RelOptInfo types

A new planner-internal package (proposed `internal/planner/path`, or a
`path_*.go` cluster inside `internal/planner`) introduces:

```go
// Cost is PG's two-number cost, in PG's units (seq_page_cost = 1.0).
type Cost struct {
    Startup float64 // cost before the first row
    Total   float64 // cost for all rows
}

// Path is one way to produce a relation, with a cost and an ordering.
type Path struct {
    Kind          PathKind   // SeqScan, Index, Hash, Nest, Merge, MultiHash, Agg, Sort, Gather, GatherMerge, ...
    Rel           *RelOptInfo
    Cost          Cost
    Rows          float64    // this path's row estimate (usually Rel.Rows)
    Pathkeys      []PathKey  // ordering guaranteed by this path (ch. 04)
    ParallelSafe  bool
    ParallelWorkers int      // > 0 only for partial paths
    RequiredOuter RelSet     // relids this path is parameterized by; empty = none (§3.1)
    Children      []*Path
    // Kind-specific fields (build side, join clauses, sort keys, probe table…)
    // carried in a small per-kind payload rather than a fat struct.
}

// RelOptInfo is a relation (base or join) and its candidate paths.
type RelOptInfo struct {
    Relids        RelSet     // the uint16 bitmask the DP already uses (bushy.go)
    Rows          float64    // computed ONCE (ch. 05); every path shares it
    Width         int        // average tuple width in bytes (ch. 05 §2)
    Pathlist      []*Path     // surviving serial candidates
    PartialPathlist []*Path   // surviving partial candidates (ch. 08)
    CheapestTotal   *Path
    CheapestStartup *Path
    // link back to the source Node(s) so create_plan can rebuild scan details
}
```

The design keeps `Path` deliberately small and the kind-specific data in a narrow
payload, because thousands of paths are allocated per join search and they live in
the planner arena. `RelSet` reuses the `uint16` relids bitmask the bushy DP
already keys on (`internal/planner/bushy.go`), so join-rel identity is shared with
the existing enumerator rather than reinvented.

### 1.1 Invariant: one source of truth for rows

`RelOptInfo.Rows` is computed once — by the `set_baserel_size_estimates` analogue
for base rels ([05](05-statistics-and-estimation-inputs.md) §1) and by the
join-size estimator for join rels ([06](06-scan-and-join-path-costs.md) §3.1) —
and **every path over the rel reads it**. Costing never calls `EstimateRows`
(`internal/planner/cardinality.go:38`) again. This is invariant #2 of the
[README](README.md), and it is what preserves Round-4's one safe property: the
cost model re-*selects* plans, it never re-*estimates* cardinality, so `rows=`
cannot move. [09](09-verification-and-acceptance.md) §1 makes this a mechanical
gate.

A subtle consequence: `Path.Rows` may differ from `Rel.Rows` (a parameterised
inner path, or a partial path whose rows are `Rel.Rows / d`), but it is always
*derived* from `Rel.Rows`, never independently estimated.

## 2. add_path and set_cheapest

`addPath(rel, newPath)` reproduces `add_path` (`postgres/…/pathnode.c:464`):
compare `newPath` against each path in `rel.Pathlist` with the fuzzy comparator,
drop any it dominates, and keep it unless dominated. `setCheapest(rel)`
reproduces `set_cheapest` (`pathnode.c:272`), scanning the surviving pathlist for
the minimum total and minimum startup.

```go
// STD_FUZZ_FACTOR = 1.01, verbatim from pathnode.c:50.
const stdFuzzFactor = 1.01

// comparePathCostsFuzzily returns which path dominates on cost, or "neither"
// when they are within the fuzz band — reproducing compare_path_costs_fuzzily
// (pathnode.c:185). The caller then breaks the tie on pathkeys and
// parallel_safe, exactly as add_path does.
func comparePathCostsFuzzily(a, b *Path, fuzz float64) costComparison
```

Dominance is five-dimensional — `disabled_nodes` (first, "trumps all else",
[02](02-pg-path-and-cost-oracle.md) §2.1), then total, startup, pathkeys,
`parallel_safe` — so a better-ordered or parallel-safe path is not discarded merely
for being 1 % dearer. In goopg `disabled_nodes` is trivially always 0 (no `enable_*`
GUCs, [02](02-pg-path-and-cost-oracle.md) §2.2), so the dimension is carried for
fidelity but never changes an outcome; the reproduced comparator still checks it so
that adding `enable_*` later is a data change, not a code change.
The fuzz factor is the reproducibility mechanism the integer→float migration needs
([07](07-cost-driven-join-order.md) §4): two orders whose real costs are within
1 % are declared equal and the tie is settled deterministically, rather than by
floating-point noise that would flicker between plan-gate runs.

## 3. create_plan: turning the winning Path into executor Nodes

This is the section with no shortcut. Selecting `rel.CheapestTotal` yields a
`*Path`; the executor runs `planner.Node`s. `createPlan(path) planner.Node`
walks the chosen path top-down (mirroring `create_plan_recurse`,
`createplan.c:388`) and emits the existing Node per path kind:

| Path kind | executor Node produced |
| --- | --- |
| SeqScan | `*planner.SeqScan` |
| Index | `*planner.IndexScan` |
| Hash | `*planner.Join{Algo: JoinAlgoHash, BuildLeft: …}` |
| Merge | `*planner.Join{Algo: JoinAlgoMerge}` + inner/outer `*planner.Sort` if unsorted |
| Nest / NLI | `*planner.Join{Algo: JoinAlgoNestLoop}` / the NLI node |
| MultiHash | `*planner.MultiHashJoin{ProbeTable: …}` |
| Agg | `*planner.Aggregate{Mode: …}` |
| Sort | `*planner.Sort` |
| Gather / GatherMerge | `*planner.Gather` / `*planner.GatherMerge` |

Three responsibilities make this more than a `switch`:

- **Sort insertion from pathkeys.** Like PG's `create_plan`, the goopg version
  inserts the `*planner.Sort` nodes the chosen pathkeys imply but that no explicit
  Sort path created — the inner sort a merge join needs, the final sort an
  `ORDER BY` needs when no path already delivered the order
  ([04](04-pathkeys-and-ordering.md) §3).
- **Scan-detail reconstruction.** The scan Path carries relids, not the fully
  built `SeqScan` with its resolved column schema and attached filters. The base
  `RelOptInfo` retains a link to the source scan Node so `create_plan` rebuilds
  the executor scan with its predicates and projection intact, rather than
  re-deriving them.
- **Nested-loop parameter threading (§3.1).** An NLI path's inner index scan is
  *parameterized* by the outer row — it re-probes the index once per outer tuple
  with the outer key bound in. `create_plan` must reconstruct that binding, the
  analogue of PG's `replace_nestloop_params` (`createplan.c`), so the emitted
  `*planner.Join{Algo: JoinAlgoNestLoop}` / NLI node re-executes the inner scan
  per outer row. Without this, the NLI path can be *costed* but not *built*.

### 3.1 Parameterized paths, scoped to NLI

`final_cost_nestloop` ([02](02-pg-path-and-cost-oracle.md) §4.6) charges "one
parameterised index probe" per outer row, and [06](06-scan-and-join-path-costs.md)
§2.3 makes index-nested-loop a costed path that must exist to recover Q4. That
requires a **parameterized path**: a `Path` whose cost and execution depend on
values supplied by an outer relation, tracked by `Path.RequiredOuter` (§1) — the
minimal analogue of PG's `ParamPathInfo` / `param_info`. The milestone scopes this
narrowly:

- only the **NLI inner index path** is parameterized (its `RequiredOuter` is the
  outer relids whose key it probes);
- `add_path` keeps a parameterized path in the rel's pathlist but it is only
  *usable* by a join that supplies its required outer — it is never a candidate for
  the rel on its own;
- everything else (seq scans, hash/merge joins, aggregates, gathers) is
  unparameterized (`RequiredOuter` empty), exactly as today.

This is a deliberately thin slice of PG's parameterized-path machinery — enough to
generate and execute the one parameterized shape TPC-H needs, no general
`ParamPathInfo` propagation across multi-level joins. Broader parameterization is
listed as deferred ([11](11-roadmap.md)). Stated explicitly because "make NLI a
costed path" is otherwise an unbuildable promise.

**Where it runs.** `create_plan` replaces the point in the pipeline where
`planSelect` today commits the join subtree (steps 3–6 of
[01](01-current-state-and-gap-analysis.md) §1). The passes *below* the join search
(scan construction, predicate localisation) feed `RelOptInfo`s; the passes
*above* it (final projection, `LIMIT`, `ORDER BY`) consume `create_plan`'s Node
output unchanged. The chosen path's `Cost` and the rel's `Width` are **copied onto
the produced Node** for `EXPLAIN` to read (§5); the `Path` objects themselves do
not escape the planner — nothing in the executor, `operators_explain.go`, or the
DDL path sees a `Path`.

**Staging note (C0–C3).** Until the DP selects by cost (C4,
[11](11-roadmap.md)), `create_plan` is fed a path *trivially built from the integer
DP's chosen order* — one path per node, no alternatives — so the Path→Node
round-trip is exercised end-to-end while `add_path`/`set_cheapest` are validated
separately by unit tests ([11](11-roadmap.md) C3). The costed pathlists are
constructed alongside from C3 but do not drive selection until C4; the switch at C4
is a change of *which* path `create_plan` receives, not of `create_plan` itself.

## 4. Where the substrate sits, and the two properties it inherits

The join search that produces pathlists runs where `tryBushyDP` runs today
(`internal/planner/planner.go` ~950). Parallel path generation, however, must
inherit the two load-bearing properties `MaybeAddGather` established
(`internal/planner/parallel.go:13-28`):

- **Post-cache, per-statement, non-mutating.** The plan cache stores the serial
  Node tree. Parallel Gather paths, and the parallelize decision, are re-derived
  *after* the cache lookup on both protocol paths
  (`internal/server/dispatch.go:1197-1207`, `dispatch_extended.go:124`), so the
  decision picks up fresh statistics without a cache write. [08](08-parallel-paths-and-degree.md)
  §3 details how the partial pathlist is reconstructed at that point without
  re-running the whole join search.

### 4.1 The ANALYZE-does-not-invalidate-the-cache gap

`pc.Invalidate()` fires only for `*planner.DDL` (`internal/server/dispatch.go:2974`),
so **`ANALYZE` does not invalidate the plan cache.** Before this bundle that was
benign — both the cached and the fresh plan were valid, differing only in a
parallel Gather. With a cost model it is sharper: a cached serial Node tree
encodes a *join order and build side* the cost model chose from whatever
statistics existed when it was first planned, and a later `ANALYZE` does not
re-plan it. The join-order cost decision is baked into the cached tree; only the
post-cache parallel decision sees fresh numbers.

This is **not a correctness bug** — every cached plan is a legal plan — but it
means the first `ANALYZE` in a session does not fully take effect until the cache
entry ages out, and a test that `ANALYZE`s and re-queries in one session may
observe a stale join order. Recorded here, and carried as a deferred item in
[11](11-roadmap.md): teaching `ANALYZE` to invalidate cache entries for the
analysed relations is the clean fix, but it is out of milestone scope and has its
own hazards (over-invalidation churn on autovacuum-driven ANALYZE).

## 5. Making EXPLAIN cost real

Today `operators_explain.go:378` prints a literal
`(cost=0.00..0.00 rows=%d width=0)` — both `cost=` and `width=` are fake; only
`rows` (from `EstimateRows`) is real. Once a plan is chosen from a costed path,
the path's `Cost{Startup, Total}` and the rel's `Width` are available and
`EXPLAIN` should print them:

```
(cost=<startup>..<total> rows=<rows> width=<width>)
```

Surfacing real cost is a deliverable this bundle owes — it is precisely what
[correlated-subquery-planning/06](../correlated-subquery-planning/06-cost-model-touchpoints.md) §6
deferred to "the 0077 line". It has two consequences worth stating:

- It forces the **width estimator** into existence
  ([05](05-statistics-and-estimation-inputs.md) §2): `width=0` can no longer be a
  literal once `cost_sort` and the size-estimate fallback both need a real width.
- It produces a **large, expected plan-gate diff** (every query's cost/width line
  changes), taken as an isolated phase ([11](11-roadmap.md) C6) so it is reviewed
  on its own rather than tangled with a semantic change. Note that plan-gate
  compares plan *shape*, not cost *numbers* ([09](09-verification-and-acceptance.md) §3),
  so goopg's costs need not equal PG's — the surfaced numbers are for human
  debuggability and internal consistency, not oracle-matching.

## 6. Divergence from PostgreSQL

- **`create_plan` emits goopg executor Nodes, not a `Plan` IR** (§3). This is the
  deliberate stopping point: goopg reproduces PG's *planning*, not a second copy
  of PG's *execution*.
- **`disable_cost` / `enable_*` GUCs are not reproduced initially** ([02](02-pg-path-and-cost-oracle.md) §2.2).
  add_path keeps the fuzzy comparison and dominance pruning; it simply has no
  disabled-node penalty to add, because goopg has no `enable_*` knobs. A later
  phase may add them for parity.
- **The plan cache is not ANALYZE-invalidated** (§4.1) — a pre-existing goopg
  divergence this bundle documents and defers rather than fixes, because the
  parallel decision (the part that most needs fresh stats) already runs
  post-cache.
- **`RelOptInfo` reuses the DP's `uint16` relid bitmask** rather than PG's
  `Bitmapset`, bounding joins at 16 relations — already the DP's limit
  (`bushy.go:80` caps at 12), so no new restriction.
