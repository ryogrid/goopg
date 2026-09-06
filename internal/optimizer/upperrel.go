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

// newUpperRelForNode allocates a FRESH upper rel of `kind`, never reusing an
// existing one — the allocator for the case PG has and goopg cannot express:
// an upper rel keyed by a NON-EMPTY relids set.
//
// PG's set-operation planning is the whole motivation. `generate_union_paths`
// and `generate_nonunion_paths` call `fetch_upper_rel(root, UPPERREL_SETOP,
// relids)` (prepunion.c:805, :1131) with the union of the leaf RT indexes the
// node covers, so `A UNION B UNION C` gets TWO distinct SETOP rels — {A,B} for
// the inner node and {A,B,C} for the outer. goopg plans set-op branches as
// opaque finished Nodes above the search seam and has no relids for them, so
// keying by 0 would file every node of a chain on ONE rel; `set_cheapest` and
// `get_cheapest_fractional_path` would then answer the second node's question
// with the first node's candidates and the fold would return the wrong subtree
// entirely. (Observed: the executor's set-op precedence suite went red the
// moment the SETOP producer shared a rel across a chain.)
//
// The synthetic `Relids` below is a DISTINCTNESS KEY, not a relation set: it
// exists only so the registry's linear search keeps finding the right rel, and
// nothing reads it as a set. That is safe for exactly the reason DESIGN §3.2
// item 3 gives — upper rels are never filed in `searchCtx.relMap` or
// `joinrels`, so no join-search code can mistake it for a base-rel mask.
// Rendered in the DPPATH trace it shows as a bit position rather than `-`,
// which is the honest signal that this rel is one of several of its kind.
func newUpperRelForNode(u *upperRels, kind UpperRelKind, tupleFraction float64) *RelOptInfo {
	if kind < 0 || kind > UpperFinal {
		panic("newUpperRelForNode: kind out of range")
	}
	rel := &RelOptInfo{
		Relids:               RelSet(len(u.rels[kind]) + 1),
		ConsiderStartup:      tupleFraction > 0,
		ConsiderParamStartup: false,
		ConsiderParallel:     false,
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
//
// DESIGN §4.3 / §9 Probe P1 disposition (C-12 review): P1 asked for a
// measurement choosing between 0 and search-root propagation. It has no
// witness in either corpus (0/100 TPC-DS sorts spill; the TPC-H Q18 spill is
// a row-estimate artefact, not a width artefact), so this third answer — a
// schema-derived heuristic — stands as PROVISIONAL until ANALYZE-backed
// widths exist above the seam. For the dominant var-width types (`text`,
// unconstrained `numeric`) it inherits `typeWidth`'s wild guess and likely
// under-states true widths: the under-charge direction, strictly better than
// the NCols=0 suppression it replaces.
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
