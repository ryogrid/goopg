package optimizer

import (
	"github.com/goopg/goopg/internal/parser"
)

// B-06 step 2 (part 1): CTE-output statistics synthesis — pure functions.
// Design: docs/design/planner-b06-cte-stats/DESIGN.md. Registry + consumer
// wiring ride in later slices; nothing here is called from production yet,
// so this file cannot change a plan (inert by construction).
//
// What is synthesized, per planned CTE body, for each output position:
//   - group keys of a grouping-sets-free Aggregate: kind=groupKey,
//     ndistinct unknown here (restriction literals are consumer-side,
//     step 3);
//   - bare aggregate outputs: kind=aggOut with the FD bound
//     (ndistinct <= group count via estimateNumGroups — one output row
//     per group for every per-group scalar agg);
//   - UNION ALL branch literals: kind=literal with the distinct-literal
//     count across branches (Q74's sale_type {'s','w'} -> 2);
//   - everything else (grouping sets, DML/recursive bodies, non-union
//     set-ops, unrecognized shapes): kind=unknown.
// Plain (non-Aggregate, non-SetOp) bodies need no synthesis: the existing
// resolver already recurses into CTE bodies for those shapes.

// cteColKind classifies one synthesized CTE-output column.
type cteColKind int

const (
	cteColUnknown cteColKind = iota
	cteColGroupKey
	cteColAggOut
	cteColLiteral
)

// cteOutputColStats is the synthesized per-column record. ndistinct < 0
// means unknown (consumer falls back to today's defaults).
type cteOutputColStats struct {
	kind      cteColKind
	ndistinct float64
}

// cteOutputStats is the per-CTE synthesis: the body row estimate plus one
// record per output column, parallel to the entry schema.
type cteOutputStats struct {
	rows float64
	cols []cteOutputColStats
}

// synthesizeCTEStats derives a CTE's output statistics from its planned
// body. Pure: reads the entry, allocates the record, touches no global or
// catalog state beyond the estimators every other sizing path uses.
func synthesizeCTEStats(entry *plannedCTE) *cteOutputStats {
	out := &cteOutputStats{}
	if entry == nil {
		return out
	}
	out.rows = float64(EstimateRows(entry.body))
	n := len(entry.schema)
	out.cols = make([]cteOutputColStats, n)
	for i := range out.cols {
		out.cols[i].ndistinct = -1
	}
	if entry.isDML {
		return out
	}
	synthAggregateOutputs(out, entry)
	synthUnionLiterals(out, entry)
	return out
}

// synthAggregateOutputs fills group-key / agg-output records when the body
// is a grouping-sets-free Aggregate under an optional Project.
// Production bodies are ALWAYS Project-wrapped (planSelectWithSettings
// wraps every non-setop select), so a bare *Aggregate match would be a
// dead rule: classification maps through Project.Targets instead — a
// ColumnRef target at output i reads aggregate-output position Index,
// which falls in the group-key region ([0, len(GroupExprs))) or the agg
// region ([len(GroupExprs), len(GroupExprs)+len(Aggs))). Reordered
// targets (SELECT count(*), g) map correctly; computed (non-ColumnRef)
// targets stay unknown. GroupingMasks columns sit between Aggs and
// Passthrough (plan.go) only when grouping sets exist, which are
// excluded below — keep the two coupled if either changes.
func synthAggregateOutputs(out *cteOutputStats, entry *plannedCTE) {
	agg, indexMap, ok := aggOutputMap(peelCTEBody(entry.body))
	if !ok || agg == nil {
		return
	}
	if len(agg.GroupingSets) > 0 {
		return
	}
	childRows := EstimateRows(agg.Child)
	groups := float64(estimateNumGroups(agg.GroupExprs, agg.Child, childRows))
	if groups < 1 {
		groups = 1
	}
	for i := range out.cols {
		apos, ok := indexMap[i]
		if !ok {
			continue
		}
		switch {
		case apos < len(agg.GroupExprs):
			out.cols[i].kind = cteColGroupKey
		case apos-len(agg.GroupExprs) < len(agg.Aggs):
			out.cols[i].kind = cteColAggOut
			out.cols[i].ndistinct = groups
		}
	}
}

// aggOutputMap resolves a (possibly Project-wrapped) body to its Aggregate
// and the output-position map: result[i] = aggregate-output position read
// by CTE output column i. Reports false when the body is not a plain
// Project-of-Aggregate (bare Aggregate counts: identity map over its own
// width) or any target is not a bare ColumnRef.
func aggOutputMap(body Node) (*Aggregate, map[int]int, bool) {
	if agg, ok := body.(*Aggregate); ok && agg != nil {
		m := make(map[int]int, len(agg.GroupExprs)+len(agg.Aggs))
		for i := 0; i < len(agg.GroupExprs)+len(agg.Aggs); i++ {
			m[i] = i
		}
		return agg, m, true
	}
	proj, ok := body.(*Project)
	if !ok || proj == nil {
		return nil, nil, false
	}
	agg, ok := proj.Child.(*Aggregate)
	if !ok || agg == nil {
		return nil, nil, false
	}
	m := make(map[int]int, len(proj.Targets))
	for i, t := range proj.Targets {
		cr, ok := t.(*ColumnRef)
		if !ok || cr == nil || cr.Index < 0 {
			continue
		}
		m[i] = cr.Index
	}
	return agg, m, true
}

// synthUnionLiterals fills literal records for UNION ALL bodies: an output
// position whose every branch projects a string literal gets the distinct
// literal count. Branches recurse (nested SetOps); any non-literal branch
// (or non-UNION-ALL set-op) vetoes the position.
func synthUnionLiterals(out *cteOutputStats, entry *plannedCTE) {
	body := peelCTEBody(entry.body)
	setop, ok := body.(*SetOp)
	// INTERSECT ALL / EXCEPT ALL are NOT unions: an intersect of 's'
	// vs 'w' is empty (nd 0), and EXCEPT output is a subset of the
	// left — claiming the across-branch count would overstate
	// ndistinct and understate selectivity (the inverse of this
	// item's purpose).
	if !ok || setop == nil || !setop.All || setop.Op != parser.SetOpUnion {
		return
	}
	for i := range out.cols {
		if out.cols[i].kind != cteColUnknown {
			continue
		}
		if lits, ok := unionBranchLiterals(body, i); ok {
			out.cols[i].kind = cteColLiteral
			out.cols[i].ndistinct = float64(len(lits))
		}
	}
}

// unionBranchLiterals collects the distinct string literals projected at
// output position pos across every UNION ALL branch. Reports false on any
// non-literal branch, non-UNION-ALL node, or unrecognized shape.
func unionBranchLiterals(body Node, pos int) (map[string]bool, bool) {
	setop, ok := peelCTEBody(body).(*SetOp)
	if !ok || setop == nil || !setop.All || setop.Op != parser.SetOpUnion {
		return branchLiteralAt(body, pos)
	}
	left, ok := unionBranchLiterals(setop.Left, pos)
	if !ok {
		return nil, false
	}
	right, ok := unionBranchLiterals(setop.Right, pos)
	if !ok {
		return nil, false
	}
	for lit := range right {
		left[lit] = true
	}
	return left, true
}

// branchLiteralAt reads a string literal at output position pos of one
// UNION branch: a Project whose target is a string literal (bare or
// typed). Anything else is not a literal.
func branchLiteralAt(body Node, pos int) (map[string]bool, bool) {
	proj, ok := peelCTEBody(body).(*Project)
	if !ok || proj == nil || pos < 0 || pos >= len(proj.Targets) {
		return nil, false
	}
	switch t := proj.Targets[pos].(type) {
	case *StringConst:
		if t == nil {
			return nil, false
		}
		return map[string]bool{t.Value: true}, true
	case *TypedStringLit:
		if t == nil {
			return nil, false
		}
		return map[string]bool{t.Value: true}, true
	}
	return nil, false
}

// peelCTEBody strips transparent wrappers (Filter/Sort/Limit) above a CTE
// body for structural classification. It deliberately does NOT descend
// into joins, scans, or subqueries: those change what the output IS.
func peelCTEBody(n Node) Node {
	for {
		switch x := n.(type) {
		case *Filter:
			if x.Child == nil {
				return n
			}
			n = x.Child
		case *Sort:
			if x.Child == nil {
				return n
			}
			n = x.Child
		case *Limit:
			if x.Child == nil {
				return n
			}
			n = x.Child
		default:
			return n
		}
	}
}
