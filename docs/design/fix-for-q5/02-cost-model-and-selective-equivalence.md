# Q5 Planner Fix 02 - Cost Model and Selective Equivalence

| field | value |
| --- | --- |
| status | draft |
| date | 2026-05-10 |
| scope | planner |
| supersedes | 0075-0001 and 0076-0004 as the primary Q5 path |

## 1. Problem statement

Phase 8 showed two separate gaps in the current planner:

1. Join costing uses raw relation size too often and is blind to the
   fact that Q5's region and orders inputs are highly selective once
   local filters are applied.
2. Global transitive equality injection is too blunt. It adds edges in
   places where the current cost model cannot price them safely.

The result is an unstable choice between:

- the existing bad `MultiHashJoin` plan, and
- a different but still bad plan with a huge lineitem-orders
  intermediate.

The fix is not "turn inferred edges back on and tune the constant".
The fix is:

1. teach the planner what each base relation will look like after its
   local filters, and
2. only synthesize the equality edges that are justified by a small or
   strongly filtered anchor.

## 2. Base-relation row estimates must be post-filter row estimates

Add a new planner structure:

```go
type baseRelInfo struct {
    bindingIdx    int
    table         *catalog.Table
    baseRows      int64
    filteredRows  int64
    localFilter   Expr
    hasLocalFilter bool
    isSmallDimension bool
}
```

Build one `baseRelInfo` per FROM binding before bushy DP runs.

Computation rule:

1. `baseRows` starts from `tableRows(tbl)`.
2. If there is no stats-bearing local filter, `filteredRows = baseRows`.
3. If there is a local filter and stats exist, first localize the
   predicate into the binding-local scan schema, then compute:

```go
localized := localizeExprToLeaf(localFilter, binding, scan)
sel := clauseSelectivityWithSource(localized, scan)
filteredRows = scaleByFloat(baseRows, sel.value)
```

4. If stats are missing or the selectivity path falls back to the
   generic default, do not invent certainty; keep the old row count.
5. For DP purposes, preserve the current bushy fallback: if `baseRows <= 0`
   because ANALYZE stats are absent, use `1` as the planner row count for
   that singleton subset rather than letting the subset collapse to `0`.

This localization step is mandatory for row estimation just as it is for
physical filter attachment. The current selectivity stack is child-local:
it reads stats through `ColumnRef.Index` against the child node's schema.
Using the unresolved global FROM-order predicate would mis-estimate or
discard later bindings such as Q5's orders and region filters.

The current planner does not expose selectivity provenance. This design
therefore requires a new helper, for example:

```go
type selectivityEstimate struct {
    value    float64
    reliable bool
}

func clauseSelectivityWithSource(pred Expr, child Node) selectivityEstimate
```

`reliable` is true only when the estimate was derived from real column
stats rather than the generic fallback. `baseRelInfo.filteredRows` may
only use the scaled value when `reliable` is true.

The intended helper shape is therefore:

```go
func estimateBaseRelInfo(
   binding rangeBinding,
   scan Node,
   local Expr,
) baseRelInfo
```

where `estimateBaseRelInfo` calls the same source-identity-safe
localization helper defined in the first design document before it asks
the selectivity layer for an estimate.

This is enough for Q5 because the two missing signals are already
supported by the current selectivity stack:

- `r_name = 'ASIA'`
- `o_orderdate` range

## 3. Join cost must include build-side cost explicitly

The current `estimateJoinCost` uses a single output-cardinality term.
That is exactly why the planner cannot distinguish a good small-build
plan from a disastrous big-build plan.

Replace it with a three-part hash-join cost:

```go
type joinCostInputs struct {
    leftRows   int64
    rightRows  int64
    outputRows int64
    buildRows  int64
    probeRows  int64
}

cost =
    outputRows * outputRowWeight +
    buildRows  * hashBuildWeight +
    probeRows  * hashProbeWeight
```

Recommended starting constants:

```go
const (
    outputRowWeight = 1
    hashBuildWeight = 4
    hashProbeWeight = 1
)
```

The actual constant values are less important than the shape. The build
term must be large enough that:

- building 1.5M orders rows is visibly more expensive than building a
  few thousand supplier or customer rows, and
- the DP prefers the plan that delays the lineitem join until the rest
  of the filtered dimension set is already formed.

This change should live in `internal/planner/bushy.go::estimateJoinCost`
and use filtered base rows for leaf subsets.

The current DP state is not sufficient for this change. Today `dpEntry`
stores only `plan` and `cost`, and `buildJoinFromDP` still re-reads
`EstimateRows(leftPlan/rightPlan)`. This design therefore requires the DP
state to carry row counts explicitly:

```go
type dpEntry struct {
   plan Node
   rows int64
   cost int64
}
```

Rule:

1. singleton subsets take `baseRelInfo.filteredRows`,
2. composed subsets take `dpEntry.rows`, not `EstimateRows(plan)`,
3. `buildJoinFromDP` chooses build side from those same stored row
   counts.

## 4. Build-side choice must use the same filtered-row inputs

`buildJoinFromDP` currently chooses build side from `EstimateRows` on the
subplans. Once filtered base rows exist, the build-side choice must be
made from the same post-filter inputs used by the DP.

For leaf subsets this means:

1. If a subset is a single filtered base relation, use
   `baseRelInfo.filteredRows`.
2. If a subset is a composed subplan, use the stored DP row estimate for
   that subset, not raw table size and not `EstimateRows(plan)`.

Without this alignment the planner can choose one binary shape but still
assign the wrong build side inside that shape.

## 5. Replace global equivalence re-enable with selective anchored inference

Do not re-enable the old global hook that appends every synthesized
equality from `inferTransitiveEqualities`.

Instead add a new helper, for example:

```go
func inferAnchoredEqualities(
    conjuncts []Expr,
    rels []baseRelInfo,
) []Expr
```

Selection rule:

1. Build the same equality classes as the existing transitive module.
2. Mark a relation as an anchor when at least one of the following is
   true:
   - `filteredRows * 2 <= baseRows`
   - the relation is `SmallDimension`
   - `filteredRows <= smallAnchorRowsThreshold` where the initial
     threshold can be 1024
3. For each equivalence class, synthesize missing equalities only from an
   anchor relation to non-anchor relations in the same class.
4. Do not synthesize equalities between two non-anchor relations.
5. Do not synthesize more than one missing edge per target relation per
   class.

This is intentionally narrower than global closure.

### Why this works for Q5

In Q5's `nationkey` class:

- `nation` is an anchor in the current codebase because it is already
   marked `SmallDimension`; join-derived reduction from filtered region is
   a later refinement, not a prerequisite for the first implementation.
- `supplier` already has an explicit `s_nationkey = n_nationkey` edge.
- `customer` is missing a direct edge to `nation`.

The anchored synthesis therefore adds only:

```text
c_nationkey = n_nationkey
```

That is exactly the missing edge needed to let the DP place customer on
the filtered nation side without also manufacturing a large set of new
high-cardinality edges elsewhere.

### Why this avoids the Q9 regression mode

Q9's problematic equality classes do not have the same shape. The
problematic classes are dominated by large fact-like relations and do not
have the same small-dimension or strongly filtered anchor pattern that
Q5 does. Under the anchored rule, Q9 keeps its existing explicit edge
set unless a future slice proves otherwise with targeted tests.

That means the selective inference rule is not "less optimization"; it is
"optimization only where selectivity has already been demonstrated".

## 6. Interaction with `MultiHashJoin`

This design expects Q5 to remain binary after the local filters are
attached. That is a feature, not a side effect.

The relationship between the pieces is:

1. local filters make region and orders selective,
2. filtered row counts change join cost,
3. anchored equality synthesis gives customer a safe path onto the
   filtered nation side,
4. filtered leaves keep `rewriteMultiWayChain` from collapsing the tree
   back into a single MHJ.

Do not treat the last point as a bug.

## 7. Proposed code anchors

The intended touch points are:

- `internal/planner/cardinality.go`
  - base relation row estimation helper
- `internal/planner/bushy.go`
  - DP input rows
  - `estimateJoinCost`
  - build-side choice alignment
- `internal/planner/equiv_class.go`
  - reuse the existing class builder, but call it from a new anchored
    synthesizer rather than the dormant global hook
- `internal/planner/planner.go`
  - feed `baseRelInfo` and anchored equalities into the DP path

## 8. Tests

Add focused tests for each rule:

1. `TestEstimateBaseRowsUsesLocalFilterSelectivity`.
2. `TestEstimateJoinCostPrefersSmallFilteredBuild`.
3. `TestInferAnchoredEqualitiesQ5NationAnchor`.
4. `TestInferAnchoredEqualitiesDoesNotBroadcastFromUnfilteredClass`.
5. `TestTryBushyDPQ5UsesAnchoredNationEdgeOnly`.

## 9. Acceptance

This slice is acceptable when:

1. Q5's plan contains a filtered region input and a filtered orders
   input.
2. Q5's plan contains a customer-to-nation join path without reintroducing
   the 303M-row lineitem-orders intermediate seen in Phase 8.
3. Q9 stays structurally unchanged, or changes only with equal-or-better
   runtime and unchanged row-count gate behavior.