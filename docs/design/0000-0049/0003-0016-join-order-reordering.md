# Cost-Based Join-Order Reordering for Comma-FROM

**Status**: accepted
**Milestone**: 0003 (HammerDB TPC-H)
**Implementation**: `internal/planner/joinorder.go`
**Tests**: `internal/planner/joinorder_test.go`,
            `internal/testutil/tpch.TestRunTPCHQueriesAgainstSyntheticData`

## Problem

The v0 planner's `planFromClause` builds a left-deep CROSS-join chain
in the user-stated source order, then `pushPredicatesIntoCrossJoins`
pushes WHERE equalities down to their qualifying Joins. That lifts
the Cartesian explosion from the outer Filter, but the *order* still
follows the SQL text. For TPC-H Q5:

```sql
SELECT n_name, sum(l_extendedprice * (1 - l_discount)) AS revenue
FROM customer, orders, lineitem, supplier, nation, region
WHERE c_custkey = o_custkey
  AND l_orderkey = o_orderkey
  AND l_suppkey = s_suppkey
  AND c_nationkey = s_nationkey
  AND s_nationkey = n_nationkey
  AND n_regionkey = r_regionkey
  AND r_name = 'ASIA'
  AND o_orderdate >= date '1994-01-01' ...
```

source order joins `customer (1500) ⋈ orders (15000) ⋈ lineitem
(60000)` first. Three large fact tables get joined before any of the
small dimension tables filter the result down. Even with hash join,
the intermediate row count balloons before nation / region pull it
back. At SF1 this materialises tens of millions of intermediate rows
that the engine then throws away.

## Decision

Add a parser-level **join-order pre-pass** that runs before column
resolution. When every comma-FROM table has ANALYZE statistics, the
pass permutes the FROM list so small-cardinality relations join
first. Operating at the parser level means we don't have to remap
any resolved `ColumnRef.Index` downstream — the entire planner sees
the new order as if the user had written it that way.

### Algorithm: greedy nearest-neighbour by cardinality

1. Look up every relation's `Stats.RowCount`. If any relation lacks
   stats, abort the rewrite (heuristic adds noise without signal).
2. Build an undirected graph from WHERE conjuncts: each
   `colref = colref` equality between columns owned by two
   different FROM-list positions becomes an edge.
3. Seed the order with the smallest-cardinality relation.
4. At each step pick the next relation that:
   a. has an equality edge to *any* relation in the joined set,
      and (among those edge-connected candidates)
   b. has the smallest row count. Ties broken by lowest index.
5. If no edge-connected relation remains, fall back to the smallest
   unjoined relation (preserves completeness — residual CROSS joins
   sit on the inside of the chain where they can no longer balloon
   freely).

### Bare-column resolution

TPC-H queries use bare references like `c_custkey = o_custkey`
rather than qualified `c.c_custkey = o.o_custkey`. The pre-pass
runs *before* the analyzer has resolved any names, so we resolve
bare column refs ourselves: build a `column-name → relation-index`
map at preprocessing time, populating only names that appear in
exactly one FROM-list table. Ambiguous names are dropped — they
wouldn't form a clean join edge anyway.

Qualified refs (`alias.col` / `table.col`) take the qualifier-based
fast path through `indexByKey`.

## Preconditions for applying the rewrite

- `len(s.FromExprs) >= 3` — nothing to reorder for two-way joins.
  The hash-join build-side selector already handles the binary case.
- Every `s.FromExprs[i].Joins` is empty — explicit `JOIN ... ON`
  syntax preserves the user's stated order. Reordering across
  explicit JOIN clauses would require honouring on-clause
  semantics that the user has explicitly placed.
- No derived tables (`Subquery != nil`). Subqueries don't carry
  catalog stats.
- Every referenced FROM-list table resolves in the catalog AND has
  `Stats != nil` with `RowCount > 0`. Falls back to source order
  if any table is unanalysed.
- Resulting permutation is non-trivial. If greedy NN produces the
  source order, skip the rewrite to avoid the slice copies.

## Why a pre-pass and not a post-resolution rewrite

The alternatives considered:

1. **Pre-pass on parser AST** (chosen). The pass runs on
   `parser.RangeVar` / `parser.Expr` before any column resolution.
   Permuting the FROM list changes nothing about column indices
   downstream — every `ColumnRef.Index` is computed from the
   *new* order. No remapping required.

2. **Post-pushdown reorder on planner.Node tree**. Would need to
   walk the resolved tree, swap Join children, and recompute
   every `ColumnRef.Index` everywhere downstream — including in
   the Aggregate's GROUP BY exprs, the Project's targets, the
   Sort's keys, the residual top-level Filter, etc. The blast
   radius is large and the index-remap is easy to get wrong.

3. **Cost-based bushy planner**. Beyond v0 scope; would also need
   real selectivity estimates (MCV / histograms), which v0
   doesn't yet collect. See
   `docs/design/0003-0003-statistics-and-cardinality.md` for the
   stats picture.

The pre-pass approach is intentionally narrow: it's a heuristic
that improves the common HammerDB TPC-H shape without taking on the
complexity of a real cost-based optimiser.

## Verified behaviour

`TestReorderCommaFromByCardinality` pins the canonical 6-table
TPC-H Q5 shape: with row counts (region=5, nation=25, supplier=100,
customer=1500, orders=15000, lineitem=60000) and the equality
predicates wiring all six tables, greedy NN seeded at region walks
`region → nation → supplier → lineitem → orders → customer`. After
`{r,n,s}` are joined, lineitem is the only edge-connected unjoined
relation (customer connects only to orders, which is also
unjoined), so lineitem picks even though customer has lower
cardinality.

`TestReorderCommaFromByCardinalityNoStats` pins the no-rewrite
fallback: missing `Stats` on any relation aborts the rewrite,
preserving source order.

`TestReorderCommaFromByCardinalitySkipsExplicitJoin` pins that
explicit `JOIN ... ON` syntax is never reordered — the join chain
stays exactly as written.

End-to-end the pass is exercised through the existing
`TestRunTPCHQueriesAgainstSyntheticData` cluster test, which runs
`ANALYZE` on every table before the 22 queries fire. All 22
continue to execute correctly with the reorder applied.

## Out of scope (deferred)

- **Bushy plans**. We always produce a left-deep chain. Real
  bushy planning needs a cost model that compares competing
  shapes, which is beyond v0's stats coverage.
- **Reorder across explicit JOIN clauses**. Honouring user-placed
  ON predicates while also reordering would require substantial
  predicate-tracking infrastructure. The current behaviour of
  "explicit JOIN preserves order" is correct and predictable.
- **Selectivity-aware ordering**. The current pick is
  cardinality-only. A future pass could weigh per-edge
  selectivity (e.g., `r_name = 'ASIA'` reduces region to 1 row,
  not 5) to refine the choice. v0's stats don't support this yet.
- **Predicate selectivity for non-equality filters**. A WHERE
  clause like `o_orderdate >= date '1994-01-01' AND
  o_orderdate < ...` already filters orders before the join, but
  the current pass doesn't push that filter into the cardinality
  estimate. With histogram stats this becomes feasible.

## References

- `internal/planner/joinorder.go` — the implementation.
- `internal/planner/pushdown.go` — the predicate-pushdown pass
  this work composes with.
- `docs/design/0003-0001-planner-overview.md` — the M0003 planner
  entry point.
- `docs/design/0003-0003-statistics-and-cardinality.md` — the
  stats infrastructure the pass relies on.
- `docs/design/0003-0002-join-executors.md` — the hash-join /
  merge-join algorithm selection that runs after the reorder.
