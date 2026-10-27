package optimizer

// M0075-0002: per-outer selectivity guard for chained-NLI
// rebind. Re-attempts the M0072-0002 design with the
// missing safeguard: before accepting a rebind that moves
// the inner IndexScan probe onto a different outer column,
// estimate the per-outer match-set size and reject the
// rebind when it would explode.
//
// Background: M0072-0002 landed the planner-side rebind
// for *NestedLoopIndexJoin outers using
// findColumnIndexByNameAndSource (M0071-0009) but was
// reverted because the resolved column was high-cardinality
// at runtime → match-set explosion → Q9 cancelled at 600 s
// with 0 rows. The guard here prevents that regression mode.
//
// Threshold rationale: nliRebindSelectivityThreshold = 100.
// The M0072-0002 failure produced match-sets in the
// thousands. 100 is conservative — well below the failure
// threshold but permissive enough to allow legitimate
// rebinds where per-outer selectivity is reasonable.
//
// See docs/design/0075-0002-q9-chained-nli-selectivity-guard.md.

const nliRebindSelectivityThreshold = 100



