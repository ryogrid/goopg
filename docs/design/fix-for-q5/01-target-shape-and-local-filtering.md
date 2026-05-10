# Q5 Planner Fix 01 - Target Shape and Local Filtering

| field | value |
| --- | --- |
| status | draft |
| date | 2026-05-10 |
| scope | planner |
| supersedes | none |

## 1. Problem statement

Q5 currently plans as a top-level `Filter` over a 6-table
`MultiHashJoin`. Two relation-local predicates remain above the join
tree:

- `r_name = 'ASIA'`
- `o_orderdate >= date '1994-01-01' AND o_orderdate < date '1995-01-01'`

That shape is fatal for Q5 because the planner loses the two most
important reductions before it decides join order:

1. `region` should become 1 row before it joins to `nation`.
2. `orders` should shrink from 1.5M rows to the 1994 slice before it
   joins to `customer` and `lineitem`.

The current planner has pieces of this logic, but not the right
combination:

- `pushPredicatesIntoCrossJoins` only pushes mixed-side predicates into
  CROSS joins; it does not attach one-table predicates below the join
  tree.
- `rewriteScanInputsWithSingleTablePredicates` can replace a scan with
  an `IndexScan`, but it does not generalize outer `Filter` predicates
  into non-indexed scan-local `Filter` nodes.
- `pushSingleSourceFiltersIntoMHJTables` only sees `mh.Filters`, not
  the outer filter that still wraps the entire Q5 join tree.

The result is that Q5 reaches `rewriteMultiWayChain` as a pure hash-join
chain over raw scans, which then collapses into `MultiHashJoin` and
throws away the staged binary shape that Q5 needs.

## 2. Goal

Introduce a planner stage that treats relation-local predicates as
first-class inputs and physically attaches a conservative subset of
them to the relevant scan inputs before `rewriteMultiWayChain` runs.

The initial pre-MHJ attachment scope is intentionally narrow:

1. one-binding predicates only,
2. no subqueries or outer references,
3. attached as `Filter(leaf)`, not as pre-MHJ `IndexScan`,
4. enabled first for the Q5-preservation path rather than as a blanket
   rewrite of every eligible query.

For Q5, the minimum acceptable post-planning leaf shapes are:

```text
Filter(r_name='ASIA')
  -> Seq Scan on region

Filter(o_orderdate in 1994)
  -> Seq Scan on orders
```

For the pre-MHJ attachment slice, the leaf must become
`Filter(SeqScan)` even if an index exists. Any later conversion to
`IndexScan` or `Filter(IndexScan)` happens only after the binary join
shape has been preserved and the existing post-planning index rewrite
pass runs. What is not acceptable is keeping those predicates above the
full 6-table join.

## 3. Proposed change

### 3.1 Add an explicit relation-local predicate partition

Add a new planner helper in a new file such as
`internal/planner/local_filters.go`:

```go
type relationLocalFilters struct {
    byBinding map[int][]Expr
}

func partitionConjunctsForJoinPlanning(
    conjuncts []Expr,
    bindings []rangeBinding,
) (joinConjuncts []Expr, locals relationLocalFilters)
```

Classification rule:

1. A conjunct is relation-local when every `ColumnRef` resolves to the
   same FROM binding.
2. A conjunct is a join conjunct when it references two or more
   bindings.
3. Any conjunct containing `OuterColumnRef`, `SubqueryExpr`,
   `ExistsExpr`, or `InExpr` with `Plan != nil` is left in the join-side
   residual set and is not treated as local.

This helper is conceptually parallel to `classifyConjunctSide`, but it
works at the FROM-binding level before a binary join tree has been
chosen.

### 3.2 Attach local predicates to scan inputs after DP planning

After `tryBushyDP` returns a binary join tree, attach the local filters
to the matching leaf inputs.

Add a second helper:

```go
func attachRelationLocalFilters(
    node Node,
    locals relationLocalFilters,
    cat catalog.Catalog,
) Node
```

For each binding-local predicate set:

1. Find the corresponding leaf scan in the chosen join tree.
2. Rebase every `ColumnRef.Index` in the local predicate from the
   global FROM-order schema into the leaf-local schema before the
   predicate is attached.
3. Preserve binding identity while rebasing: aliases and self-joins must
   continue to bind to the same source relation they resolved against in
   the original predicate.
4. Wrap the leaf in `Filter{Child: leaf, Predicate: localizedLocal}`.

The rebasing step is mandatory. Current planner code only moves
predicates into narrower scopes after rebasing indices, for example in
`remapKeyToSubset` and `pushSingleSourceFiltersIntoMHJTables`. This new
pass must follow the same rule. A correct implementation therefore needs
an explicit localization helper, for example:

```go
func localizeExprToLeaf(e Expr, binding rangeBinding, leaf Node) Expr
```

That helper must rewrite indices by binding/source identity, not by
column name alone.

This is a planner-stage operation, not an executor optimization. The
binary join tree must literally contain the filtered leaves.

### 3.3 Deliberately keep filtered leaves opaque to `rewriteMultiWayChain`

Do not teach `collectMultiHashTables` to see through `Filter(SeqScan)`
leaves for this work. The current behavior, where a filtered leaf makes
the chain ineligible for `MultiHashJoin` packing, is desirable for Q5.

That behavior should be promoted from an accident to an explicit
contract:

1. Filter-wrapped scan leaves are a binary-plan preservation barrier.
2. Q5 relies on that barrier.
3. A future attempt to flatten filtered leaves back into MHJ must come
   with a separate design that proves the staged reductions are not
   lost.

This means the expected Q5 end state is a binary hash-join tree, not a
different flavor of `MultiHashJoin`.

### 3.4 Keep pre-MHJ index tightening out of the first slice

The current planner intentionally delays scan tightening until after MHJ
formation through `rewriteScanInputsWithSingleTablePredicates` and the
MHJ-specific rewrite helpers. To avoid broad, hard-to-price planner
diffs, this design keeps that split:

1. before `rewriteMultiWayChain`: attach only `Filter(leaf)` wrappers,
2. after `rewriteMultiWayChain`: allow the existing index-rewrite path to
   convert `Filter(SeqScan)` into `Filter(IndexScan)` when the final plan
   remains binary.

This keeps the early slice focused on preserving Q5's binary shape
rather than broadening index-selection behavior at the same time.

## 4. Planner flow changes

The relevant section of `internal/planner/planner.go` should become:

```go
pred, _ := resolveExpr(s.Where, ctx)
conjuncts := splitAnd(pred)

joinConjuncts, localFilters :=
    partitionConjunctsForJoinPlanning(conjuncts, ctx.bindings)

node = &Filter{Child: node, Predicate: combineAnd(joinConjuncts)}
node = tryBushyDP(... using joinConjuncts and localFilters metadata ...)
node = pushPredicatesIntoCrossJoins(node) // existing mixed-side path stays
node = unnestSubqueriesInPlan(node)       // preserve current planner stage order
node = attachRelationLocalFilters(node, localFilters, cat)
node = rewriteMultiWayChain(node, cat)
```

Important details:

1. relation-local filters should still be available to the join-ordering
   logic even before they are attached physically,
2. residual mixed-side predicates must still go through the existing
   `pushPredicatesIntoCrossJoins` path,
3. only true one-binding predicates are eligible for the leaf-attachment
   pass.

The row-count side of local filters is specified in the second design
document.

### 4.1 Initial rollout gate

Although the mechanism is general, the first landing should not switch
on pre-MHJ attachment for every eligible query at once. Add an explicit
planner-side gate, for example:

```go
func shouldAttachBeforeMHJ(
   binding rangeBinding,
   local Expr,
   fromCount int,
   relInfo baseRelInfo,
) bool
```

Initial rule:

1. `fromCount >= 5`, and
2. the relation is `SmallDimension` or has a reliably selective local
   filter estimate.

That keeps Slice A aligned with its stated purpose: preserve Q5's binary
plan family first, then widen only after the plan-diff and focused gate
results are understood.

For clarity: the pre-MHJ slice never performs index selection. Every
predicate admitted by `shouldAttachBeforeMHJ` is attached as a
`Filter(leaf)` wrapper, even if a later post-MHJ pass could still tighten
that leaf into `Filter(IndexScan)`.

## 5. Q5-specific effect

After this change alone, but before new cost work, the planner should be
able to preserve leaves shaped like:

```text
Filter(r_name='ASIA') -> Seq Scan(region)
Filter(o_orderdate in 1994) -> Seq Scan(orders)
Seq Scan(nation)
Seq Scan(supplier)
Seq Scan(customer)
Seq Scan(lineitem)
```

That is already a strict improvement over the M0076 baseline because it
prevents the all-raw-scan MHJ packing path.

## 6. Scope control

This change must be conservative.

The attachment pass must not:

1. Cross aggregate or window boundaries.
2. Cross outer joins.
3. Reclassify subquery-bearing predicates as local.
4. Rebind a predicate to a different alias of the same table.

It may operate only on:

1. FROM-level base bindings chosen in the current SELECT.
2. Pure one-binding predicates.
3. `SeqScan` and `IndexScan` leaves already present in the chosen plan.
4. An initial narrow slice where pre-MHJ attachment is limited to the
   predicates needed to preserve Q5's binary plan family.

## 7. Tests

Add focused planner tests:

1. `TestPartitionConjunctsForJoinPlanningQ5` - Q5 shape yields local
   filters for `region` and `orders`, and join conjuncts for the six
   equijoins.
2. `TestAttachRelationLocalFiltersLocalizesIndices` - rebasing from
   global FROM-order indices into leaf-local indices is correct.
3. `TestAttachRelationLocalFiltersPreservesAliasIdentity` - self-join and
   alias cases stay bound to the right leaf.
4. `TestAttachRelationLocalFiltersWrapsNonIndexedLeaf` - a one-table
   predicate without a usable index becomes `Filter(SeqScan)`.
5. `TestRewriteMultiWayChainSkipsFilteredLeaves` - a hash-join chain
   with at least one filtered leaf is intentionally left as binary
   joins.

## 8. Acceptance

Q5 is acceptable for this slice when all of the following are true:

1. The plan no longer contains `Multi-Way Hash Join (6 tables)`.
2. The region and orders predicates appear below the join tree, not
   above it.
3. Other queries remain structurally identical unless they also have
   true one-binding predicates that this slice is expected to relocate.