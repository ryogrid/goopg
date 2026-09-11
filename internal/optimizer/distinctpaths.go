package optimizer

// The DISTINCT upper rel — planner-refactor take3 C-16 (P4-07).
//
// `create_distinct_paths` (planner.c:4816) is the fourth Phase-4 upper
// producer after C-15's GROUP_AGG: for the finished DISTINCT input it
// offers hashed and unique-over-sorted `PathDistinct` candidates on the
// `(DISTINCT, NULL)` upper rel, prices each, and lets `add_path` select.
// There is no distinct rule to retire — SELECT DISTINCT planned as a bare
// `&Distinct{}` wrapper (M0097-0005), and `distinctOp` hash-dedups either
// shape — so this cut only ADDS a choice where none existed.
//
// Design: docs/design/planner-p4-distinct-paths/DESIGN.md. The shape is
// option (b) as C-12/C-15: the finished child is wrapped in a
// `PathPrebuilt` seed over the DISTINCT rel (input rows/cost), candidates
// stack above it, `setCheapest` runs, and `createPlanNode` on the winner
// emits the node through the new `PathDistinct` arm (`createDistinctPlan`,
// createplansimple.go).
//
// Two slices share this file: C-16a (hashed vs unique choice) and
// C-16b (unique-over-sorted via `DistinctOn` reuse — `distinctOnOp`
// already streams adjacent dedup, and both node kinds already render
// `"Unique"`, so ~0 executor LOC). C-16b is NOT a second producer: it is
// the second candidate below plus the arm mapping.

import (
	"github.com/goopg/goopg/internal/catalog"
)

// distinctProducer strings for the DPPATH trace (pathtrace.go). With
// `Relids = 0` the lines read `producer=upper.distinct.* relids=-`, the
// same convention C-12/C-15 established.
const (
	distinctHashedProducer = "upper.distinct.hashed"
	distinctUniqueProducer = "upper.distinct.unique"
)

// createDistinctPaths is `create_distinct_paths` for the one DISTINCT
// goopg plans above the seam: the finished `distinctNode` (spec built at
// the wrapper site). It returns the winning node — `*Distinct`, or
// `*DistinctOn` with all-output-columns keys when the unique candidate
// wins — or a `PlanError` when no candidate exists (PG's "could not
// implement DISTINCT"; unreachable — hashed is always offered — defensive
// as C-15).
//
// DISTINCT ON never reaches here: both wrapper sites gate on
// `len(s.DistinctOn) == 0` (defense-in-depth; both parsers leave
// `Distinct=false` for DISTINCT ON today).
func createDistinctPaths(u *upperRels, distinctNode *Distinct, cat catalog.Catalog, ps PlannerSettings, tupleFraction float64) (Node, error) {
	if distinctNode == nil {
		return nil, &PlanError{Code: "XX000", Message: "createDistinctPaths: nil distinct node"}
	}
	if u == nil {
		u = newUpperRels()
	}
	cp := ps.costParams()
	distinctRel := fetchUpperRel(u, UpperDistinct, 0, tupleFraction)
	sizeDistinctRelFromNode(distinctRel, distinctNode)

	child := distinctNode.Child
	inputRows := float64(EstimateRows(child))
	if inputRows < 0 {
		inputRows = 0
	}
	seed := newPrebuiltPath(distinctRel, child)
	seed.Rows = inputRows
	if pc := legacyDisplayCostOf(child); pc.PlanRows > 0 || pc.TotalCost > 0 {
		seed.Cost = Cost{Startup: pc.StartupCost, Total: pc.TotalCost}
	}

	addDistinctPaths(distinctRel, seed, distinctNode, child, cp, ps)
	setCheapest(distinctRel)

	best := getCheapestFractionalPath(distinctRel, tupleFraction)
	if best == nil {
		return nil, &PlanError{Pos: distinctNode.Pos(), Code: "0A000",
			Message: "could not implement DISTINCT"}
	}
	node, _ := createPlanNode(best)
	if node == nil {
		return nil, &PlanError{Pos: distinctNode.Pos(), Code: "XX000",
			Message: "createDistinctPaths: PathDistinct built no node"}
	}
	// The arm builds a FRESH node (spec copy for Distinct, DistinctOn for
	// Unique) — unlike C-15 there is no aliasing to preserve, because the
	// spec was just built at the wrapper site and nothing else references
	// it. Call sites adopt the return value.
	switch node.(type) {
	case *Distinct, *DistinctOn:
		return node, nil
	}
	return nil, &PlanError{Pos: distinctNode.Pos(), Code: "XX000",
		Message: "createDistinctPaths: PathDistinct built no distinct node"}
}

// sizeDistinctRelFromNode sizes the DISTINCT rel: Rows from P1-25's
// `estimateDistinctRows` (grouping over every output column — F3, clamped
// ≥ 1); Width/NCols/AvgVarBytes describe the output (the §4.3 duty; no
// spill arm exists for distinct).
func sizeDistinctRelFromNode(rel *RelOptInfo, distinctNode *Distinct) {
	if rel == nil || distinctNode == nil {
		return
	}
	cols := distinctNode.Output()
	rows := estimateDistinctRows(cols, distinctNode.Child)
	if rows < 1 {
		rows = 1
	}
	rel.Rows = float64(rows)
	rel.Width = nodeTupleWidth(distinctNode)
	rel.NCols = len(cols)
	rel.AvgVarBytes = nodeAvgVarBytes(cols)
}

// distinctOutputSatisfiesOrder reports whether a DISTINCT node's output
// already delivers a required ORDER BY, making the M0097-0046 outer Sort
// redundant (R84). Sound iff ALL of the following hold:
//
//   - out is *Distinct (type-gate, fail-closed): its executor,
//     distinctOp, hash-dedups then ALWAYS re-sorts ascending,
//     NULLs last, over all columns — input order is destroyed,
//     so the delivered order is exactly (c0 ASC NL, c1 ASC NL,
//     …). *DistinctOn streams input order instead (different
//     operator, different argument — out of scope here).
//   - every required key is ASC with nulls-last (read from the
//     effective SortKey entries — sortByNullsFirst already
//     applied — never the raw parser flags).
//   - the required keys are a positional prefix of the output:
//     key i is a ColumnRef with Index == i, for positions
//     0..n-1 in order. A full-row lexicographic ASC/NL order
//     satisfies exactly its prefixes — ORDER BY (c1) alone is
//     NOT satisfied by a (c0,c1) ordering.
//
// Anything else keeps the Sort. In particular DESC, explicit
// NULLS FIRST, non-ColumnRef keys, and non-prefix keys all
// decline — root-0036/DESC behavior is preserved by
// construction.
func distinctOutputSatisfiesOrder(out Node, outerKeys []SortKey) bool {
	if _, ok := out.(*Distinct); !ok {
		return false
	}
	if len(outerKeys) == 0 {
		return false
	}
	for i, k := range outerKeys {
		if k.Desc || k.NullsFirst {
			return false
		}
		cr, ok := k.Expr.(*ColumnRef)
		if !ok || cr.Index != i {
			return false
		}
	}
	return true
}

// distinctAllColKeys is one ascending SortKey per output column — the input
// order a streaming dedup consumes (and the Sort the producer stacks when
// the input does not deliver it).
func distinctAllColKeys(child Node) []SortKey {
	cols := child.Output()
	keys := make([]SortKey, 0, len(cols))
	for i, c := range cols {
		keys = append(keys, SortKey{
			Expr:       &ColumnRef{Index: i, Name: c.Name, Type: c.Type},
			Desc:       false,
			NullsFirst: false,
		})
	}
	return keys
}

// distinctAllKeyCols is every output position — the `DistinctOn.KeyCols`
// for the unique candidate (full-row dedup).
func distinctAllKeyCols(child Node) []int {
	n := len(child.Output())
	cols := make([]int, 0, n)
	for i := 0; i < n; i++ {
		cols = append(cols, i)
	}
	return cols
}

// distinctCost prices the dedup work both DISTINCT forms share: PG's Unique
// price (`cpu_operator` per input row for the adjacent comparison +
// `cpu_tuple` per output row) on top of the input. The hashed form pays the
// same terms — the executor hash-dedups per row either way — so hashed vs
// unique differ only in their INPUT price (seed vs Sort), never here.
// Startup carries the input's startup plus the per-row compare (the Sort
// blocks anyway, so streaming buys nothing here — stated, not modeled).
func distinctCost(inputStartup, inputTotal, inputRows, outputRows float64, cp costParams) Cost {
	startup := inputStartup + cp.cpuOperatorCost*inputRows
	total := inputTotal + cp.cpuOperatorCost*inputRows + cp.cpuTupleCost*outputRows
	return Cost{Startup: startup, Total: total}
}

// addDistinctPaths is the per-input body of `create_final_distinct_paths`
// for goopg's one input: hashed always, unique-over-sorted always (over
// the producer-stacked Sort — input order guaranteed by construction).
// Single candidate per shape by construction.
func addDistinctPaths(distinctRel *RelOptInfo, seed *Path, distinctNode *Distinct, child Node, cp costParams, ps PlannerSettings) {
	inputRows := seed.Rows
	numDistinct := distinctRel.Rows

	// HASHED: the executor hash-dedups the seed as-is (today's behavior).
	// `enable_hashagg = off` marks it DisabledNodes (B-17a preference,
	// never skip) instead of deleting it.
	addPath(distinctRel, &Path{
		Kind: PathDistinct, Distinct: distinctNode,
		Rel: distinctRel, Rows: numDistinct,
		DisabledNodes: disabledNodesFor(!ps.EnableHashAgg, seed),
		Cost: costAgg(cp, AggStrategyHashed, inputRows, seed.Cost.Startup, seed.Cost.Total,
			len(child.Output()), numDistinct, 0, 0, 0),
		Children: []*Path{seed},
	}, distinctHashedProducer)

	// UNIQUE over the producer-stacked Sort (streaming adjacent dedup).
	// This is the ONLY Sort-driven candidate: a "sorted Distinct" (hash
	// dedup over the same Sort) would price and order identically to it
	// and be rejected as a duplicate by add_path — offering both would be
	// noise, not choice. PG likewise builds Unique, not sorted-Agg, for
	// the sorted DISTINCT shape.
	sortInput := sortPathForBounded(seed, pathkeysForSortKeys(distinctAllColKeys(child)), cp, -1)
	uniqueCost := distinctCost(sortInput.Cost.Startup, sortInput.Cost.Total, inputRows, numDistinct, cp)
	addPath(distinctRel, &Path{
		Kind: PathDistinct, Distinct: distinctNode, Unique: true,
		Rel: distinctRel, Rows: numDistinct,
		DisabledNodes: sortInput.DisabledNodes,
		Cost:          uniqueCost,
		Pathkeys:      sortInput.Pathkeys, Children: []*Path{sortInput},
	}, distinctUniqueProducer)
}
