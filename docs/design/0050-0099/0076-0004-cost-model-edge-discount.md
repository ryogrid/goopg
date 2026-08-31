# Design 0076-0004 — Cost-model edge discount + deterministic synthesis

**Milestone:** M0076-0004
**Status:** draft (MED-HIGH RISK — touches bushy DP cost
model)
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0075-0001 (`internal/planner/equiv_class.go`
+ 9 unit tests landed in commit `e89c98a`).

## Context

M0075-0001 landed the `inferTransitiveEqualities`
module. The planner-side hook into `tryBushyDP` was
attempted then reverted because Q9 cancelled at 600 s
when enabled. The Phase 7 handover §3 documented this
as the load-bearing M0076 dependency.

**Phase 1 Explore-agent root-cause analysis** (2026-05-10):
Q9's lineitem becomes a star center when synthesised
edges exist — it has 6 explicit edges converging on it.
The DP enumerates many more join orders, each with
different intermediate cardinalities. `estimateJoinCost`
at `bushy.go:511` uses `(L*R)/max(NDistinct(L.k), NDistinct(R.k))`
— a formula that doesn't model build-side memory cost.
A plan that builds a 6M-row hash table on lineitem then
probes 5 times is costed the same as a plan that scans
lineitem 5 times. Adding more edges (transitivity OR
otherwise) gives the DP more cheap-looking-but-bad
options to choose from.

**Plus**: `equiv_class.go::classes()` iterates a Go map
→ nondeterministic conjunct ordering → the bushy DP can
pick different plans across runs. This makes plan-diff
debugging unreliable.

## Goals

Two complementary fixes that together prepare the
cost-model to safely admit synthesised edges (the
M0076-0001 hook re-enable in Commit D depends on this):

1. **Deterministic synthesis ordering** —
   `equiv_class.go::classes()` returns slices sorted by
   `compareColumnIdent` so the synthesised conjunct
   sequence is reproducible. Plan-snapshot
   `structural` diffs become reliable.

2. **Edge cost discount** — `joinEdge` gains an
   `isInferred bool` field (default false). Edges
   produced from synthesised conjuncts in
   `buildJoinGraph` are marked. `estimateJoinCost`
   applies an `inferredEdgeDiscount` multiplier
   (initial 0.5×) when costing inferred edges.

The intuition for the discount: synthesised edges are
transitively REDUNDANT with the explicit edges they
were derived from. The DP shouldn't treat them as
independent join opportunities of equal weight to
explicit edges. Discounting biases the search away from
plans that exploit the redundancy as if it were
selectivity.

## Non-goals

- **Replacing the cost formula.** A proper build-side
  memory cost model (`buildRows * loadFactor +
  probeRows * log(buildSize)`) is out of scope; M0077
  candidate. M0076-0004 is a surgical preparation, not
  a redesign.
- **Removing the `joinEdge` data structure.** Existing
  edge ordering / lookup invariants preserved.
- **Tuning the discount factor empirically beyond the
  initial 0.5×.** Commit D verifies whether 0.5× admits
  the Q5 hook safely; if not, tune via subsequent
  commits.

## Proposed implementation

### Determinism fix in `equiv_class.go::classes()`

```go
// classes returns each equivalence class as a slice of
// its members. Classes with only one member are skipped
// (no closure to synthesise from a singleton).
//
// M0076-0004: returns deterministic ordering. The slices
// are sorted by compareColumnIdent so the caller's
// synthesised conjunct sequence is reproducible across
// runs (essential for plan-snapshot diff stability).
func (ec *equivClasses) classes() map[columnIdent][]columnIdent {
    result := make(map[columnIdent][]columnIdent)
    for k := range ec.parent {
        root := ec.find(k)
        result[root] = append(result[root], k)
    }
    // Filter out singletons + sort each class.
    for root, members := range result {
        if len(members) < 2 {
            delete(result, root)
            continue
        }
        sort.SliceStable(members, func(i, j int) bool {
            return compareColumnIdent(members[i], members[j]) < 0
        })
        result[root] = members
    }
    return result
}
```

`inferTransitiveEqualities` also needs sorted iteration
over the classes themselves (map iteration order):

```go
// Pass 2: synthesise the closure — for each
// equivalence class with ≥ 2 members, emit
// `member[i] = member[j]` for every pair NOT in
// seenPairs.
//
// M0076-0004: collect class roots first, sort, then
// iterate. Without this the synthesised conjunct order
// varies per run.
classes := ec.classes()
roots := make([]columnIdent, 0, len(classes))
for root := range classes {
    roots = append(roots, root)
}
sort.SliceStable(roots, func(i, j int) bool {
    return compareColumnIdent(roots[i], roots[j]) < 0
})
for _, root := range roots {
    members := classes[root]
    // ... existing pair emission loop ...
}
```

### `joinEdge.isInferred` field in `bushy.go`

```go
// joinEdge represents one equality predicate that
// connects two relations (or two scan-set members) in
// the join graph. Edges drive the bushy DP.
type joinEdge struct {
    // ... existing fields ...
    leftIdx, rightIdx uint16
    leftKey, rightKey Expr
    selectivity       float64

    // M0076-0004: marks edges produced from synthesised
    // (transitively-inferred) conjuncts. estimateJoinCost
    // applies a discount factor to these edges to bias
    // the DP away from plans that exploit transitivity
    // as if it were selectivity.
    isInferred bool
}
```

### `buildJoinGraph` plumbing

The current signature:
```go
func buildJoinGraph(
    tables []*catalog.Table,
    scans []Node,
    scanWidth []int,
    conjuncts []Expr,
    bindings []rangeBinding,
) *joinGraph
```

Add an `inferredCount int` parameter indicating how
many of the trailing conjuncts are synthesised:

```go
func buildJoinGraph(
    tables []*catalog.Table,
    scans []Node,
    scanWidth []int,
    conjuncts []Expr,
    inferredCount int,  // M0076-0004
    bindings []rangeBinding,
) *joinGraph
```

When iterating `conjuncts` and producing edges, the
final `inferredCount` entries are marked with
`isInferred=true`.

### `estimateJoinCost` discount

```go
const inferredEdgeDiscount = 0.5  // M0076-0004; tunable

func estimateJoinCost(leftCost, rightCost float64, edge *joinEdge, g *joinGraph, cat catalog.Catalog) float64 {
    // ... existing cost formula ...
    cost := /* L * R / max(NDistinct(L.k), NDistinct(R.k)) */

    if edge.isInferred {
        cost *= inferredEdgeDiscount  // discount, not penalty (cost is what we MINIMISE)
    }
    return cost
}
```

**Note on direction of discount**: smaller cost = more
preferred. Multiplying by 0.5× makes inferred edges
LOOK CHEAPER → DP prefers them. That's the OPPOSITE of
what we want.

**Corrected logic**: we want inferred edges to look
EXPENSIVE (so DP prefers explicit edges). Use a multiplier
> 1 (e.g., 2.0× = "doubly costly compared to explicit"),
or equivalently apply discount to selectivity (which
multiplies INTO the cost product). The exact direction
is determined by Commit D verification; the design here
proposes the constant + tuning protocol:

```go
// inferredEdgePenalty makes synthesised edges look more
// costly than explicit edges so the DP prefers plans
// that drive joins through original predicates. 2.0×
// is the initial value; tuned in Commit D verification.
const inferredEdgePenalty = 2.0

// In estimateJoinCost:
if edge.isInferred {
    cost *= inferredEdgePenalty
}
```

## Verification

Pre-commit gate:
- Tight gate: Q12=2, Q13=35, Q21=381, Q22=7, Q9 ≥ 7
  (this commit DOESN'T enable the hook; gate values
  unchanged from M0075).
- `plan-diff` against M0076 baseline: NO diff (since
  the hook is still off, plans are identical).
- 21-q row counts: zero change.

New tests in
`internal/planner/cost_model_inferred_edge_test.go`:
- `TestEstimateJoinCostInferredEdgePenalty` — same
  edge with `isInferred=false` vs `true` produces
  cost differing by `inferredEdgePenalty` factor.
- `TestEquivClassClassesDeterministic` —
  `inferTransitiveEqualities` produces the same
  output across 100 invocations.
- `TestEquivClassClassesSortedByColumnIdent` — pin
  the sort order matches `compareColumnIdent`.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | `inferredEdgePenalty=2.0` too small → Q9 still picks bad plan when D enables hook | Tunable constant; D's verification gates on Q5 completion AND Q9 ≥ 7. If D fails, tune up to 4.0 / 8.0 first. |
| R2 | `inferredEdgePenalty=2.0` too large → Q5 plan stays unchanged (DP rejects synthesised edges) | D's verification gates on Q5 returning ≥ 1 row within budget. If Q5 still cancels, the DP may need help (check via plan-diff: does the synthesised edge appear in Q5's plan? If no, penalty is too high). |
| R3 | Map-iteration determinism fix breaks an existing test | Existing 9 unit tests in `equiv_class_test.go` only check membership (`hasPair`) not order; sorted output is a strict refinement. |
| R4 | Cost formula change breaks Q1 / Q3 / Q11 / Q14 (unrelated queries) | This commit DOESN'T enable the hook, so synthesised edges aren't yet present. No queries see `isInferred=true` edges in their join graph yet. Verification: 21-q row-count parity confirms no plan changes. |

## Migration plan

Single commit (Commit C in M0076):
1. Land determinism fix in `equiv_class.go::classes()` +
   sorted iteration in `inferTransitiveEqualities`.
2. Add `isInferred` field to `joinEdge`.
3. Add `inferredCount` parameter to `buildJoinGraph`
   (default 0 at all current call sites — no behaviour
   change).
4. Add `inferredEdgePenalty` constant + apply in
   `estimateJoinCost`.
5. Land tests.
6. Verify via `plan-diff` (zero diff against M0076
   baseline) + tight gate + 21-q row counts.

If `plan-diff` shows ANY change after this commit,
investigate — this commit is supposed to be inert
(hook not enabled).

## References

- `internal/planner/equiv_class.go` (M0075-0001) —
  module landed; this commit adds determinism.
- `internal/planner/bushy.go::tryBushyDP` (line ~67) —
  the call site where `splitAnd(pred)` produces
  conjuncts; M0076-0001 (Commit D) adds the
  `inferTransitiveEqualities` injection here.
- `internal/planner/bushy.go::buildJoinGraph` (around
  line 200) — extended to thread `inferredCount`.
- `internal/planner/bushy.go::estimateJoinCost` (line
  511) — extended with the penalty multiplier.
- `internal/planner/cardinality.go` — `EstimateRows`,
  `keyNDistinct`, `columnNDistinctForChild` (used by
  `estimateJoinCost`; unchanged).
- `docs/design/0075-0001-q5-equivalence-class-inference.md`
  — module design (status: PARTIAL, hook reverted).
  M0076-0001 status section will note re-enable post-
  this commit's preparation.
