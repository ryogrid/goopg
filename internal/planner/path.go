package planner

// Path / RelOptInfo / add_path / set_cheapest — the substrate of the PostgreSQL
// cost model, reproduced for goopg. See docs/design/cost-model/, chapters 02 (the
// PG oracle) and 03 (the goopg substrate).
//
// This file is phase C0.1: the pure data types plus add_path/set_cheapest, with
// NO integration into the live planning pipeline. Nothing calls it from
// planSelect yet, so it cannot change a plan; it is validated in isolation by
// path_test.go. Cost functions arrive in C3, path generation in C3/C4,
// create_plan in C0.2.
//
// The one non-obvious fidelity point reproduced here is PG 18's dominance order:
// compare_path_costs_fuzzily (postgres/src/backend/optimizer/util/pathnode.c:185)
// compares disabled_nodes FIRST ("trumps all else", :191), then total cost, then
// startup cost, each within a multiplicative STD_FUZZ_FACTOR (:50) tolerance.
// add_path (:464) folds in pathkeys, parallel_safe, and required-outer relids.

// stdFuzzFactor is PG's STD_FUZZ_FACTOR (pathnode.c:50): two costs within 1% are
// treated as equal, and the tie is broken on the non-cost dimensions. This is the
// determinism mechanism the integer->float migration needs (design ch. 07 §4).
const stdFuzzFactor = 1.01

// RelSet is a set of base-relation ids, one bit per relation. It reuses the
// uint16 bitmask the bushy DP already keys joinrels on (bushy.go), so joinrel
// identity is shared with the existing enumerator rather than reinvented. This
// bounds a single query's join at 16 base relations — already the DP's regime
// (it caps at 12, bushy.go:80).
type RelSet uint16

// Cost is PG's two-number cost, in PG's units (seq_page_cost = 1.0, design ch. 02
// §3). Startup is the cost expended before the first row emerges; Total is the
// cost to return all rows. A cheaper-total path can lose to a cheaper-startup one
// under a LIMIT, which is why both axes are carried (design ch. 07 §1.2).
type Cost struct {
	Startup float64
	Total   float64
}

// PathKind identifies which physical operator a Path represents. createPlan
// (C0.2) switches on it to emit the corresponding executor Node.
type PathKind int

const (
	// PathPrebuilt wraps an already-constructed executor Node. It is the C0
	// bridge: while path generation does not yet exist, the join subtree the
	// integer DP builds is wrapped in a PathPrebuilt so createPlan has something
	// to translate and the Path<->Node round-trip is exercised end-to-end
	// (design ch. 03 §3.1 staging note). Later phases add the real kinds below
	// and PathPrebuilt is retired for join/scan rels.
	PathPrebuilt PathKind = iota
	PathSeqScan
	PathIndexScan
	PathHashJoin
	PathMergeJoin
	PathNestLoop
	PathMultiHash
	PathAgg
	PathSort
	PathGather
	PathGatherMerge
)

// Path is one way to produce a relation, with a cost and an ordering. It is kept
// deliberately small — thousands are allocated per join search — with kind-
// specific data in a narrow payload rather than a fat struct (design ch. 03 §1).
type Path struct {
	Kind PathKind
	Rel  *RelOptInfo

	Cost Cost
	Rows float64

	// Pathkeys is the ordering this path guarantees (design ch. 04). Empty in
	// C0/C1 for every path until sort / ordered-scan / merge paths exist.
	Pathkeys []PathKey

	// ParallelSafe / ParallelWorkers describe parallel eligibility. Workers > 0
	// only for partial paths (design ch. 08 §2). Unused until C5.
	ParallelSafe    bool
	ParallelWorkers int

	// DisabledNodes reproduces PG 18's path->disabled_nodes (the count of
	// enable_*-disabled nodes below this path). goopg has no enable_* GUCs, so it
	// is always 0 today; carried so the dominance order matches PG and adding
	// enable_* later is a data change, not a code change (design ch. 02 §2.2).
	DisabledNodes int

	// RequiredOuter is the set of outer relations a parameterized path depends on
	// (the minimal analogue of PG's ParamPathInfo, design ch. 03 §3.1). Empty for
	// every ordinary path; non-empty only for an NLI inner index path (C3).
	RequiredOuter RelSet

	Children []*Path

	// node is the executor Node a PathPrebuilt wraps. nil for every other kind.
	node Node
}

// RelOptInfo is a relation — base or join — and its candidate paths. Rows is the
// single source of truth for this relation's cardinality: it is computed once
// (design ch. 05 §1) and every path over the rel reads it; costing never
// re-estimates (design ch. 03 §1.1, invariant #2).
type RelOptInfo struct {
	Relids RelSet
	Rows   float64
	Width  int

	Pathlist        []*Path
	PartialPathlist []*Path

	CheapestTotal   *Path
	CheapestStartup *Path
}

// newRelOptInfo creates a rel with the given relids and (once-computed) size.
func newRelOptInfo(relids RelSet, rows float64, width int) *RelOptInfo {
	return &RelOptInfo{Relids: relids, Rows: rows, Width: width}
}

// newPrebuiltPath wraps an already-built executor Node as a Path over rel. Cost
// is left zero in C0 (selection is still the integer DP, so the cost is not
// consulted); Rows is taken from the rel. This is the C0 bridge (design ch. 03
// §3.1).
func newPrebuiltPath(rel *RelOptInfo, n Node) *Path {
	return &Path{Kind: PathPrebuilt, Rel: rel, Rows: rel.Rows, node: n}
}

// pathCostComparison is the result of compare_path_costs_fuzzily.
type pathCostComparison int

const (
	costsEqual     pathCostComparison = iota // within fuzz on both axes
	costsBetter1                             // path1 is fuzzily cheaper
	costsBetter2                             // path2 is fuzzily cheaper
	costsDifferent                           // each is better on one axis
)

// comparePathCostsFuzzily reproduces compare_path_costs_fuzzily
// (pathnode.c:185). disabled_nodes trumps all else (:191); then total cost is
// checked before startup (many paths have zero startup); each within `fuzz`.
func comparePathCostsFuzzily(p1, p2 *Path, fuzz float64) pathCostComparison {
	// Number of disabled nodes, if different, trumps all else (pathnode.c:191).
	if p1.DisabledNodes != p2.DisabledNodes {
		if p1.DisabledNodes < p2.DisabledNodes {
			return costsBetter1
		}
		return costsBetter2
	}

	// Total cost first — more likely to differ (pathnode.c:203).
	if p1.Cost.Total > p2.Cost.Total*fuzz {
		// p1 fuzzily worse on total; if p2 is fuzzily worse on startup they are
		// genuinely different, else p2 dominates.
		if p2.Cost.Startup > p1.Cost.Startup*fuzz {
			return costsDifferent
		}
		return costsBetter2
	}
	if p2.Cost.Total > p1.Cost.Total*fuzz {
		if p1.Cost.Startup > p2.Cost.Startup*fuzz {
			return costsDifferent
		}
		return costsBetter1
	}
	// Fuzzily equal on total; decide on startup.
	if p1.Cost.Startup > p2.Cost.Startup*fuzz {
		return costsBetter2
	}
	if p2.Cost.Startup > p1.Cost.Startup*fuzz {
		return costsBetter1
	}
	return costsEqual
}

// dimensionCmp is a per-dimension comparison: dimBetter1 means path1 is at least
// as good and better on this axis, dimEqual means indistinguishable, dimBetter2
// the reverse, dimIncomparable means neither dominates on this axis.
type dimensionCmp int

const (
	dimEqual dimensionCmp = iota
	dimBetter1
	dimBetter2
	dimIncomparable
)

func costDim(c pathCostComparison) dimensionCmp {
	switch c {
	case costsBetter1:
		return dimBetter1
	case costsBetter2:
		return dimBetter2
	case costsDifferent:
		return dimIncomparable
	default:
		return dimEqual
	}
}

// boolDim compares two eligibility booleans where true is "better" (e.g.
// parallel_safe): true dominates false.
func boolDim(a, b bool) dimensionCmp {
	switch {
	case a == b:
		return dimEqual
	case a && !b:
		return dimBetter1
	default:
		return dimBetter2
	}
}

// outerDim compares required-outer relid sets: a path requiring FEWER outer
// relations (a subset) is less constrained and therefore better; unrelated sets
// are incomparable. Reproduces the bms_subset logic add_path uses for
// parameterized paths (pathnode.c).
func outerDim(a, b RelSet) dimensionCmp {
	if a == b {
		return dimEqual
	}
	aSubB := a&b == a // a ⊆ b
	bSubA := a&b == b // b ⊆ a
	switch {
	case aSubB:
		return dimBetter1 // a is less constrained
	case bSubA:
		return dimBetter2
	default:
		return dimIncomparable
	}
}

// pathRelation is the overall dominance relationship between two paths across all
// four dimensions (cost, pathkeys, parallel_safe, required-outer).
type pathRel int

const (
	relIncomparable pathRel = iota // each better on some axis -> keep both
	relEqual                       // indistinguishable on every axis
	relADominates                  // a is no worse everywhere and better somewhere
	relBDominates
)

// comparePaths reduces the per-dimension comparisons to a single relationship. A
// dominates B iff A is no worse than B on every dimension and strictly better on
// at least one; if A is better on one axis and B on another they are
// incomparable and both survive.
func comparePaths(a, b *Path) pathRel {
	dims := []dimensionCmp{
		costDim(comparePathCostsFuzzily(a, b, stdFuzzFactor)),
		comparePathkeysDim(a.Pathkeys, b.Pathkeys),
		boolDim(a.ParallelSafe, b.ParallelSafe),
		outerDim(a.RequiredOuter, b.RequiredOuter),
	}
	hasA, hasB := false, false
	for _, d := range dims {
		switch d {
		case dimIncomparable:
			return relIncomparable
		case dimBetter1:
			hasA = true
		case dimBetter2:
			hasB = true
		}
	}
	switch {
	case hasA && hasB:
		return relIncomparable // a trade-off across axes
	case hasA:
		return relADominates
	case hasB:
		return relBDominates
	default:
		return relEqual
	}
}

// addPath reproduces add_path (pathnode.c:464): keep newPath unless an existing
// path dominates it (or exactly ties it), and drop any existing path newPath
// dominates. On an exact tie the incumbent is kept and newPath rejected, so
// duplicates do not accumulate — matching PG's practical behaviour of keeping the
// first of two indistinguishable paths.
func addPath(rel *RelOptInfo, newPath *Path) {
	rel.Pathlist = addToPathlist(rel.Pathlist, newPath)
}

// addPartialPath is add_partial_path (pathnode.c:798): the same dominance pruning
// over the partial pathlist, used for parallel candidates (design ch. 08 §2).
// Present now; exercised from C5.
func addPartialPath(rel *RelOptInfo, newPath *Path) {
	rel.PartialPathlist = addToPathlist(rel.PartialPathlist, newPath)
}

func addToPathlist(list []*Path, newPath *Path) []*Path {
	// If any existing path dominates or exactly ties the newcomer, reject it and
	// leave the list untouched (a rejected path cannot itself dominate anything,
	// by transitivity of the dominance order).
	for _, old := range list {
		switch comparePaths(newPath, old) {
		case relBDominates, relEqual:
			return list
		}
	}
	// The newcomer survives; drop every incumbent it strictly dominates.
	survivors := list[:0]
	for _, old := range list {
		if comparePaths(newPath, old) != relADominates {
			survivors = append(survivors, old)
		}
	}
	return append(survivors, newPath)
}

// setCheapest reproduces set_cheapest (pathnode.c:272): pick the minimum-total
// and minimum-startup path for the rel. Ties are broken deterministically by
// pathlist position (the earliest-added wins), so plan choice is stable across
// runs.
func setCheapest(rel *RelOptInfo) {
	rel.CheapestTotal = nil
	rel.CheapestStartup = nil
	for _, p := range rel.Pathlist {
		if rel.CheapestTotal == nil || p.Cost.Total < rel.CheapestTotal.Cost.Total {
			rel.CheapestTotal = p
		}
		if rel.CheapestStartup == nil || p.Cost.Startup < rel.CheapestStartup.Cost.Startup {
			rel.CheapestStartup = p
		}
	}
}
