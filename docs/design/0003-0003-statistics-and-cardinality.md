# Statistics and Cardinality Estimation (Milestone 0003)

| Field         | Value                                                  |
| ------------- | ------------------------------------------------------ |
| Status        | draft                                                  |
| Date          | 2026-04-28                                             |
| Milestone     | 0003 — HammerDB TPC-H Workload                         |
| Refines       | [root-0011-planner.md](root-0011-planner.md), [0003-0010-analyze-statistics.md](0003-0010-analyze-statistics.md) |
| Supersedes    | —                                                      |
| Superseded by | [0006-0001-sampling-and-mcv-histograms.md](0006-0001-sampling-and-mcv-histograms.md) (statistics collection); planner consumption supersession tracked under `0006-0003` / `0006-0004` |

## Problem

ANALYZE now writes per-table reltuples and per-column
NDistinct (see `0003-0010-analyze-statistics.md`). The
cost-based planner work needs those numbers to make
join-ordering and algorithm-selection decisions. This loop
adds the bottom-up cardinality-estimation pass that consumes
catalog stats and produces per-node row-count estimates,
surfaced through EXPLAIN.

The estimates aren't yet *used* for planner decisions —
hash-join build-side selection, join-order reordering, and
algorithm switches stay on their existing rules-based logic.
This loop establishes the infrastructure and verifies the
math via EXPLAIN; future loops can flip the cost-model
switch.

## Upstream reference

- `postgres/src/backend/optimizer/path/costsize.c` —
  `estimate_rel_size`, `clauselist_selectivity`,
  `eqjoinsel`.
- `postgres/src/backend/utils/adt/selfuncs.c` — per-operator
  selectivity heuristics. v0 borrows
  `DEFAULT_INEQ_SEL = 1/3` and the `1/200` no-stats equality
  fallback.

## Decisions

### One free function, no Node-interface bloat

Adding a `RowEstimate()` method to the Node interface would
force every node type to override (Insert, DDL, Transaction,
…). Instead, `EstimateRows(n Node) int64` is a free function
that switches on the concrete type. Nodes that don't have a
useful estimate return 0 — explicit "no estimate" rather than
a misleading 1.

### Estimates flow bottom-up

Each plan-node case in `EstimateRows`:

| Node       | Estimate                                       |
| ---------- | ---------------------------------------------- |
| SeqScan    | `tbl.Stats.RowCount` or 0                      |
| IndexScan  | 1 (single equality probe)                      |
| Values     | exact len                                      |
| Filter     | `child * defaultGenericSelectivity` (= 1/3)    |
| Limit      | `min(child, IntegerConst.Limit)` when constant |
| Sort       | child unchanged                                |
| Project    | child unchanged                                |
| Join       | see "Join cardinality" below                   |
| Aggregate  | NDistinct(group key) when single ColumnRef key, else child/2 |
| Insert     | EstimateRows(Source)                           |
| Update / Delete / DDL / Utility / etc. | 0           |

When child returns 0, the parent also returns 0 — a missing
estimate stays missing all the way up. The EXPLAIN renderer
suppresses the `(rows=N)` annotation when N is 0, matching
upstream's "no costs without ANALYZE" behaviour.

### Join cardinality: `|L| * |R| / max(NDistinct)`

Upstream's `eqjoinsel` for a disjoint-equality predicate is:

    selectivity = 1 / max(NDistinct(L.k), NDistinct(R.k))
    rows        = |L| * |R| * selectivity

v0 uses the same formula when `Join.Algo == JoinAlgoHash`
(which means the planner has already split the predicate into
disjoint LeftKey/RightKey pairs). NDistinct is looked up
through `columnNDistinctForChild` — currently only SeqScan
provides a useful answer; Filter/Sort pass through, Project
returns 0 (column-index reversal isn't implemented yet).

When NDistinct isn't available, fall back to
`|L| * |R| * defaultEqSelectivity` (= 0.005 = 1/200, matching
upstream's no-stats fallback).

CROSS join is the cartesian product — no predicate.

Outer joins are handled symmetrically with INNER for now;
upstream upper-bounds LEFT join cardinality to `max(|L|,
inner-estimate)` but that refinement waits.

### Aggregate cardinality: NDistinct on the group key

`SELECT … GROUP BY single_col` is the common shape. v0
returns the column's NDistinct directly when the group
expression is a `ColumnRef` against a stats-bearing scan.
Multi-column GROUP BY and expression-based grouping fall
back to `child / 2` — pessimistic but bounded.

`SELECT count(*) FROM t` (no GROUP) is exactly 1 row.

### Selectivity defaults: upstream-aligned

Three constants:

```go
defaultEqSelectivity      = 0.005       // = 1/200, upstream's no-stats fallback
defaultIneqSelectivity    = 1.0/3.0     // upstream's DEFAULT_INEQ_SEL
defaultGenericSelectivity = 1.0/3.0     // generic Filter without recognised shape
```

`Filter` doesn't yet decompose its predicate into a sided-
equality, so it always uses the generic 1/3 multiplier. A
follow-up loop can teach `Filter` the `col = const` shape
and apply the eq-selectivity rule there too.

### EXPLAIN integration

`internal/executor/operators_explain.go`'s `walkPlan` now
appends `(rows=N)` to each node's label when
`planner.EstimateRows(n) > 0`. Format matches upstream's
text-format EXPLAIN well enough that operators can reason
about the planner's view of the workload.

Example after ANALYZE:

```
Projection (rows=4)
  ->  Hash Join (INNER) (rows=4)
    ->  Seq Scan on customer (rows=4)
    ->  Seq Scan on orders (rows=4)
```

Without ANALYZE, all `(rows=N)` annotations are absent:

```
Projection
  ->  Hash Join (INNER)
    ->  Seq Scan on customer
    ->  Seq Scan on orders
```

## Verification

End-to-end via psql 18.3:

| Query                                                           | Estimate |
| --------------------------------------------------------------- | -------: |
| EXPLAIN SELECT c_name FROM customer JOIN orders ON …            |        4 |
| EXPLAIN SELECT count(*) FROM orders WHERE o_custkey = 1         |        1 |
| EXPLAIN SELECT * FROM customer ORDER BY c_custkey LIMIT 2       |        2 |

(For the join: |customer|=4, |orders|=4, NDistinct(c_custkey)=4, NDistinct(o_custkey)=3 → 4*4/4 = 4.)

## Out of scope (deferred to subsequent loops)

- Cost model proper (CPU + I/O cost units, not just row counts).
  Needed to compare hash-join vs. nested-loop, sort vs.
  hash-aggregate, etc.
- Predicate-decomposition selectivity (`col = const`,
  `col < const`, etc.) inside Filter. Today Filter blanket-
  uses 1/3.
- Histograms / MCVs for skewed-data range selectivity.
- Multivariate stats / extended statistics (`CREATE
  STATISTICS` upstream).
- Join-order reordering. The cost is now visible via
  EstimateRows but the planner still emits left-deep chains
  in source order. Reordering needs a search algorithm
  (Selinger-style dynamic programming, or a greedy
  heuristic).
- Build-side selection for hash join — swap left/right when
  right is much larger. Requires fixing up downstream column
  indexes, which propagates through the resolve phase.

## Cross-references

- ANALYZE producer side:
  [0003-0010-analyze-statistics.md](0003-0010-analyze-statistics.md).
- EXPLAIN renderer:
  [0003-0007-explain.md](0003-0007-explain.md).
- Hash-join algorithm choice:
  [0003-0002-join-executors.md](0003-0002-join-executors.md).
