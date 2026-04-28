# Join Executors (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-28                                             |
| Milestone  | 0003 — HammerDB TPC-H Workload                         |
| Refines    | [root-0011-planner.md](root-0011-planner.md), [root-0012-executor.md](root-0012-executor.md) |
| Supersedes | —                                                      |

## Problem

The M1 planner emits one join algorithm — nested-loop. For TPC-H
at SF1, the smallest tables (NATION, REGION) have 25 and 5 rows;
the largest (LINEITEM) has ~6 million. A query like Q3
(customer ⋈ orders ⋈ lineitem) over those scales would do
~6 trillion comparisons under nested-loop, vs. ~6 million
hashes + probes under hash-join.

Hash join is the foundational join algorithm goopg needs before
attempting Q1–Q22. Follow-up loops added sort-merge join so
RIGHT/FULL outer equality joins can avoid the quadratic nested
loop fallback while preserving outer-row semantics.

## Upstream reference

- `postgres/src/backend/optimizer/path/joinpath.c` —
  `match_unsorted_outer`, `hash_inner_and_outer`.
- `postgres/src/backend/executor/nodeHashjoin.c` —
  `ExecHashJoin` state machine.
- `postgres/src/backend/utils/hash/dynahash.c` — chained
  hash table; v0 uses Go's built-in map.

## Decisions

### One operator, three algorithms

`joinOp` (in `internal/executor/operators_join_agg.go`) keeps a
single Open/Next/Close shape. Open dispatches:

```go
if o.plan.Algo == planner.JoinAlgoHash {
  return o.runHashJoin(...)
}
if o.plan.Algo == planner.JoinAlgoMerge {
  return o.runMergeJoin(...)
}
return o.runNestedLoop(...)
```

This matches the M1 buffering semantics — all algos drain
their inputs into in-memory slices in Open and emit from a
materialised result slice in Next. Streaming hash-probe (which
upstream uses to avoid buffering the outer side) is deferred —
the v0 buffer-everything strategy is consistent with the
nested-loop op and keeps the algorithm switch local.

### Planner-side detection: single equality, disjoint sides

`splitEqualityForHash(pred, leftWidth)` inspects the join
predicate. The hash algorithm is enabled only when:

1. The top-level predicate is a `BinaryOp` with `Op == "="`.
2. One side references columns whose `Index < leftWidth`
   (the left input only); the other side references columns
   whose `Index >= leftWidth` (the right input only).
3. Constants and other side-free leaves are allowed on either
   side.

`exprSide(e, leftWidth)` is the side-classifier: `sideUnknown`
for pure constants, `sideLeft` / `sideRight` for ColumnRefs,
recursing through BinaryOp / UnaryOp / FuncCall / CaseExpr /
ExtractExpr; `sideMixed` short-circuits the promotion.

When the equality is `right.col = left.col`, the planner flips
the operands so `LeftKey` always references the left input and
`RightKey` the right. This keeps the executor one-direction.

What stays on nested-loop:
- AND-of-equalities (`a.x = b.x AND a.y = b.y`). v0 doesn't
  yet split conjunctions into multi-column hash keys.
- Inequalities, range predicates, subqueries.
- CROSS join (no predicate) and any join with a NULL predicate.

RIGHT/FULL equality joins now take the merge path. The planner
keeps hash join for INNER/LEFT and chooses merge join for
RIGHT/FULL when `splitEqualityForHash` succeeds.

### Merge join for RIGHT/FULL equality joins

`runMergeJoin` computes key Datums for both sides (`LeftKey`,
`RightKey`), sorts each side with `compareDatum`, then merges the
two streams. Equal-key runs are expanded as a Cartesian product
(so duplicates on either side preserve SQL join multiplicity).
NULL keys never match and are emitted only as unmatched rows for
outer-join shapes.

This gives O((N+M) log (N+M)) behaviour for RIGHT/FULL equi-joins
instead of nested-loop's O(N*M), without changing planner surface
area (same Join node, different `Algo`).

### Build vs. probe side

The right input is the default build side. The plan structure
is "left ⋈ right" with predicates resolved against the
left||right schema concat; hashing on the right means a single
map keyed by the right input's join column.

For INNER joins the planner now consults `EstimateRows` (see
`docs/design/0003-0003-statistics-and-cardinality.md`) and sets
`Join.BuildLeft = true` when the LEFT input is the smaller side.
The executor's `runHashJoinBuildLeft` then hashes the LEFT input
and probes from the RIGHT, but emits rows in the same canonical
[leftCols, rightCols] order so downstream operators see no
schema change. EXPLAIN surfaces the choice as
`Hash Join (INNER, build=left)` when the swap fires.

LEFT JOIN keeps the right-as-build default unconditionally: its
outer-row preservation walks the LEFT side as the probe stream,
so we can't flip the build side without re-deriving the
unmatched-row emission logic. RIGHT/FULL never reach the hash
algorithm — they take merge join — so build-side selection is
strictly an INNER-hash optimisation.

Build-side selection is gated on having stats: when both sides'
estimates are 0 (no ANALYZE has run on either table) the planner
keeps `BuildLeft = false`, matching the prior behaviour.

### Hash key encoding: datumKey reuse

`evalHashKey` evaluates the key expression against a padded
row (left||null on the build side, left||null on the probe
side, since RightKey/LeftKey reference only their respective
half) and hands the resulting Datum to `datumKey`, which is
the same canonical-string form `aggregateOp` uses for
GROUP BY keys. NULL keys never match — both sides skip the
hash insert/probe, which mirrors upstream's NULL-aware
equi-join semantics.

## Verification

End-to-end against `goopg start -D <dir>` with upstream psql
18.3:

```
CREATE TABLE customer (c_custkey INT, c_name TEXT);
CREATE TABLE orders (o_orderkey INT, o_custkey INT, o_totalprice INT);
INSERT INTO customer VALUES (1,'alice'),(2,'bob'),(3,'carol'),(4,'dave');
INSERT INTO orders VALUES (10,1,100),(11,1,50),(12,2,200),(13,4,300);

SELECT c_name, o_orderkey FROM customer JOIN orders ON c_custkey = o_custkey;
-- alice/10, alice/11, bob/12, dave/13   (4 rows, hash algo)

SELECT c_name, o_orderkey FROM customer LEFT JOIN orders ON c_custkey = o_custkey;
-- carol gets a NULL right side.        (5 rows, hash algo)

SELECT c_name FROM customer JOIN orders ON o_custkey = c_custkey;
-- Right-then-left equality flipped at plan time. (4 rows, hash algo)
```

`TestPlanJoinPicksHashAlgo` pins which predicates promote and
which stay on nested-loop. `TestPlanJoinHashBuildSidePicksSmaller`
pins the build-side selection: smaller-LEFT → BuildLeft=true,
bigger-LEFT → BuildLeft=false (default), no-stats → default,
LEFT JOIN → default regardless of side sizes.

## Out of scope (deferred to subsequent loops)

- Sort-merge join (the third upstream algorithm). For TPC-H
  for INNER/LEFT joins. v0 currently reserves merge join for
  RIGHT/FULL equality shapes; cost-based selection between hash
  and merge for INNER/LEFT is deferred.
- Multi-column equality keys (`a.x = b.x AND a.y = b.y` →
  composite hash key).
- Cost-based build-side selection (smaller-input → build).
  Implemented for INNER hash joins as of 2026-04-29 (see "Build
  vs. probe side" above). LEFT JOIN still pins right-as-build
  for outer-row semantics; INNER + bidirectional outer hash
  with build-side selection is a future loop.
- RIGHT / FULL outer hash join.
- Streaming probe (the upstream "outer side iterates while
  hash table builds incrementally" pattern). v0 buffers both
  sides for simplicity; the lower memory ceiling for huge
  outers comes with the same bookkeeping streaming probe
  needs.
- Hash aggregate as a planner-level rule (the executor's
  aggregate operator already groups by datumKey, so the
  algorithmic side is done; only the planner's OPERATOR
  selection — Aggregate-on-Hash vs. Aggregate-on-Sort — is
  what's missing).

## Cross-references

- TPC-H Q3 / Q5 / Q9 query bodies: HammerDB upstream
  `tpc-h/queries-93-orig.sql`.
- Existing nested-loop fallback:
  [root-0012-executor.md](root-0012-executor.md).
- M1 join planning:
  [root-0011-planner.md](root-0011-planner.md).
