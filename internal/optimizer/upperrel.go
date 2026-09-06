package optimizer

import "github.com/goopg/goopg/internal/catalog"

// Upper relations — planner-refactor take3 C-11 (P4-02).
//
// PG plans everything above the scan/join level as a pipeline of "upper"
// RelOptInfos (`grouping_planner`, planner.c:1868ff): set-op → grouping →
// window → distinct → ordered → final, each consuming the previous rel's
// pathlist through `add_path` and `set_cheapest`. goopg has none of that above
// its search root — `planJoinlistSearch` publishes a Node and every
// aggregate / window / ORDER BY / LIMIT above it is a wrap-on-top Node rewrite
// (`docs/design/planner-p4-upper-rels/DESIGN.md` §2 is the inventory).
//
// This file is the registry those rewrites are retired into, one item at a
// time: C-12 gives `UpperOrdered` its first real path, C-15/C-16/C-18 fill
// the others. It is `fetch_upper_rel` (relnode.c:1458-1497) transcribed, with
// three decisions the design settled (§3.2):
//
//  1. The registry is PER PLANNING SCOPE — one per `planSelectWithSettings`
//     invocation, so a FROM-subquery, a CTE body and a view body each get
//     their own. That is the C-10d boundary expressed in data: a derived
//     table is planned as an opaque leaf, and an upper rel can no more reach
//     into it than an outer LIMIT can (relfromjoinlist.go:26-46). It is NOT
//     on `searchCtx`, which exists per search PROBLEM and there may be several
//     per statement.
//  2. `Relids` is 0 for every upper rel: `fetch_upper_rel(root, KIND, NULL)`,
//     which is what every in-tree PG caller passes ("the meaning of the Relids
//     set is not specified here", relnode.c:1449). `relSetBits` already renders
//     0 as `relids=-`, so a DPPATH line for an upper-rel path is unambiguous
//     with no trace-format change.
//  3. Upper rels are NEVER filed in `searchCtx.relMap` or `joinrels`.
//     `makeJoinRel` is a find-or-create over `relMap` and `finalRel` asserts
//     exactly one rel at the top level; an upper rel in either would corrupt
//     the search from above. The registry has no pointer to a searchCtx at
//     all, which is what makes the invariant structural rather than policed.

// UpperRelKind is `UpperRelationKind` (pathnodes.h:69-81), value for value.
type UpperRelKind int

const (
	UpperSetOp           UpperRelKind = iota // result of UNION/INTERSECT/EXCEPT, if any
	UpperPartialGroupAgg                     // result of partial grouping/aggregation, if any
	UpperGroupAgg                            // result of grouping/aggregation, if any
	UpperWindow                              // result of window functions, if any
	UpperPartialDistinct                     // result of partial "SELECT DISTINCT", if any
	UpperDistinct                            // result of "SELECT DISTINCT", if any
	UpperOrdered                             // result of ORDER BY, if any
	UpperFinal                               // result of any remaining top-level actions
	// UpperFinal must stay the last entry: it sizes `upperRels`
	// (pathnodes.h:80 "NB: UPPERREL_FINAL must be last enum entry").
)

var upperRelKindNames = [...]string{
	UpperSetOp:           "SETOP",
	UpperPartialGroupAgg: "PARTIAL_GROUP_AGG",
	UpperGroupAgg:        "GROUP_AGG",
	UpperWindow:          "WINDOW",
	UpperPartialDistinct: "PARTIAL_DISTINCT",
	UpperDistinct:        "DISTINCT",
	UpperOrdered:         "ORDERED",
	UpperFinal:           "FINAL",
}

func (k UpperRelKind) String() string {
	if k >= 0 && int(k) < len(upperRelKindNames) {
		return upperRelKindNames[k]
	}
	return "UPPERREL(?)"
}

// upperRels is `PlannerInfo.upper_rels[UPPERREL_FINAL + 1]` (pathnodes.h:391):
// one plain list per kind, searched linearly by relids. "No code outside this
// function should assume anything about how to find a particular upperrel"
// (relnode.c:1455) — every reader goes through `fetchUpperRel`.
type upperRels struct {
	rels [UpperFinal + 1][]*RelOptInfo
}

func newUpperRels() *upperRels { return &upperRels{} }

// fetchUpperRel is `fetch_upper_rel` (relnode.c:1458-1497): find-or-create
// the upper rel of `kind` for `relids`. `tupleFraction` is `root->tuple_fraction`,
// read here for the one thing an upper rel carries from it —
// `consider_startup = (root->tuple_fraction > 0)` ("cheap startup cost is
// interesting iff not all tuples to be retrieved"). Everything else is the
// zero value PG also starts from: no paths, no cheapest, not parallel, and
// `consider_param_startup = false`.
//
// Size fields (`Rows`, `Width`, `NCols`, `AvgVarBytes`) are NOT set here —
// PG's upper rel starts with an empty `reltarget` too — because the registry
// has no input to size from; the producer that gives the rel its first path
// sizes it from that path's input (`sizeUpperRelFromNode`). DESIGN §4.3 says
// why a producer may not skip that step.
func fetchUpperRel(u *upperRels, kind UpperRelKind, relids RelSet, tupleFraction float64) *RelOptInfo {
	if kind < 0 || kind > UpperFinal {
		panic("fetchUpperRel: kind out of range")
	}
	for _, rel := range u.rels[kind] {
		if rel.Relids == relids {
			return rel
		}
	}
	rel := &RelOptInfo{
		Relids:               relids,
		ConsiderStartup:      tupleFraction > 0,
		ConsiderParamStartup: false,
		ConsiderParallel:     false, // "might get changed later" (relnode.c:1484)
	}
	u.rels[kind] = append(u.rels[kind], rel)
	return rel
}

// sizeUpperRelFromNode populates the size fields an upper rel's cost functions
// read, from the finished Node the rel's first path is built over.
//
// This is the load-bearing extra duty DESIGN §4.3 found: `costSortRun` gates
// its external-merge arm on `ncols > 0` ("column count unknown" suppresses the
// disk charge, deliberately — an unknown width must not invent I/O), and a
// fresh `RelOptInfo` has `NCols == 0`. An `ORDERED` rel that skipped this step
// would price TPC-H Q18's top-level sort — `rows=1565307 width=204`, the
// largest in the suite — as an in-memory quicksort. Silently: nothing fails,
// the number is merely wrong in the direction that hides a spill.
//
//   - Rows ← the legacy estimator. There is no other row count for a Node
//     above the seam until C-20a retires `EstimateRows`; a search-produced
//     child carries the path's own count on its `PlanCost`, and
//     `legacyDisplayCostOf` reads that first, so the two agree wherever both
//     exist. Temporary read, named as such.
//   - Width ← `nodeTupleWidth`, the same width EXPLAIN prints for the node.
//   - NCols ← the schema length, which is what the executor's `len()` sees.
//   - AvgVarBytes ← the variable-width columns' share of that same width
//     model (`nodeAvgVarBytes`). Probe P1 (DESIGN §9) — measuring the
//     upper rel's real payload against a spill file — has no witness in
//     either corpus (0 of 100 TPC-DS sorts spill; TPC-H's Q18 sort is a
//     row-estimate artefact, 57 actual rows), so this takes the third of
//     the design's candidates, the one that is neither 0 (under-states, the
//     bug direction) nor the search-root rel's (over-states after an
//     aggregate collapses the row), and is derived from the schema the node
//     actually emits rather than argued for.
func sizeUpperRelFromNode(rel *RelOptInfo, child Node) {
	if rel == nil || child == nil {
		return
	}
	cols := child.Output()
	pc := legacyDisplayCostOf(child)
	rel.Rows = pc.PlanRows
	rel.Width = nodeTupleWidth(child)
	rel.NCols = len(cols)
	rel.AvgVarBytes = nodeAvgVarBytes(cols)
}

// nodeAvgVarBytes is the variable-width share of `tupleWidth(cols)`: the sum of
// `typeWidth` over the columns `typeIsFixedWidth` rejects. It is the
// `RelOptInfo.AvgVarBytes` a base rel takes from ANALYZE's per-column
// `AvgWidth`, for a node that has no ANALYZE to read — the same
// `get_typavgwidth` fallbacks `typeWidth` already uses for the width column.
func nodeAvgVarBytes(cols []SchemaColumn) float64 {
	var sum float64
	for _, c := range cols {
		if typeIsFixedWidth(c.Type) {
			continue
		}
		sum += float64(typeWidth(c.Type))
	}
	return sum
}

// typeIsFixedWidth is `typlen > 0` for the types `typeWidth` (relsize.go)
// prices at a fixed size. The two switches are siblings and must agree: a
// type moved from one arm of `typeWidth` to the other moves here too.
func typeIsFixedWidth(t catalog.Type) bool {
	if t.IsArray {
		return false
	}
	switch t.Name {
	case "bool", "boolean",
		"int2", "smallint",
		"int4", "integer", "int", "date", "float4", "real", "oid",
		"int8", "bigint", "float8", "double precision", "double",
		"money", "time", "timestamp", "timestamptz", "timestamp with time zone",
		"timestamp without time zone",
		"name":
		return true
	}
	return false
}
