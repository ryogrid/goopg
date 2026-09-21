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
	// cteColPassthrough is an output column a row-preserving body hands
	// through from its own input unchanged — today only a `*WindowAgg`'s
	// child columns. It is NOT the same claim as the others: a group key is
	// a SUBSET of its input's values, whereas a pass-through column is the
	// input's values exactly, so its ndistinct is an equality, not a bound.
	cteColPassthrough
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
	synthWindowOutputs(out, entry)
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
			out.cols[i].ndistinct = groupKeyNDistinct(agg.GroupExprs[apos], agg.Child, groups)
		case apos-len(agg.GroupExprs) < len(agg.Aggs):
			out.cols[i].kind = cteColAggOut
			out.cols[i].ndistinct = groups
		}
	}
}

// groupKeyNDistinct is the group-combo rule (B-06 design gap G2, and this
// file's own step 3): the distinctness of ONE grouping column in the
// aggregate's OUTPUT.
//
// The rule is `min(input ndistinct, group count)`, and it applies ONLY when
// the input ndistinct is known. Grouping never invents values — `GROUP BY g`
// emits a subset of the values `g` already had — so the input distinctness is
// an upper bound; and the output has one row per group, so the group count is
// another. The minimum of the two is the estimate.
//
// The group count alone is NOT a usable fallback, and this was measured
// rather than reasoned. An earlier version of this rule used it when the
// input was unknown: sound as a BOUND, but wrong as an ESTIMATE for one key
// of a multi-key grouping, because a low-cardinality column
// (TPC-DS `d_week_seq`, a few hundred weeks) grouped alongside a
// high-cardinality one gets priced at the whole group count. On the default
// arm that inflated the key's ndistinct far above the truth, which deflated
// the join selectivity that divides by it: TPC-DS Q59 went from 43 estimated
// rows to 1 and flipped Hash Join -> Nested Loop. Values stayed correct, so
// only the plan channel caught it.
//
// So an unknown input yields `unknown` and today's defaults, matching this
// file's standing rule — never a guess. A non-`*ColumnRef` grouping
// expression is a computed value whose input distinctness cannot be read, and
// is likewise unknown.
//
// Re-entrancy: this calls back into `columnNDistinctForChild`, which can
// reach `cteSynthNDistinct` and therefore another CTE's `outputStats()`. A
// self-referential body is safe by construction because `outputStats` marks
// `synthDone` BEFORE computing, so a re-entrant call observes a nil record
// and the consumer declines — keep that ordering if either side is edited.
func groupKeyNDistinct(groupExpr Expr, child Node, groups float64) float64 {
	cr, ok := groupExpr.(*ColumnRef)
	if !ok {
		return -1
	}
	in := columnNDistinctForChild(cr.Index, child)
	if in <= 0 {
		return -1
	}
	nd := float64(in)
	if nd > groups {
		nd = groups
	}
	if nd < 1 {
		return -1
	}
	return nd
}

// synthWindowOutputs fills pass-through records when the body is a
// (possibly Project-wrapped) `*WindowAgg`.
//
// M0145-0009 slice 3. This is the largest unknown population the slice-1
// census found: `Project(WindowAgg)` bodies accounted for 366 of the 648
// unclassified asks per TPC-DS SF0.25 run, 56% of them, with no rule at all.
//
// The rule is unusually strong, and the reason is worth stating. A WindowAgg
// is ROW-PRESERVING — it emits exactly one output row per input row and
// publishes `child row ++ func outputs` — so a column it hands through is not
// merely bounded by its input's distinctness, it HAS the input's
// distinctness. No clamp is needed or correct: clamping to anything would
// understate a column whose values are literally unchanged.
//
// Window-function outputs themselves (positions at or past the child's
// width) stay unknown: `rank()`, `sum() OVER ...` and friends compute new
// values whose distinctness this file cannot derive. PG needs no equivalent
// rule because `examine_simple_variable` (selfuncs.c) resolves a subquery
// output Var to the underlying relation's statistics regardless of the nodes
// in between; goopg's resolver stops at the CTE boundary, which is gap G1.
//
// Fail-closed like its siblings: a non-bare-ColumnRef Project target, a
// position past the child's width, or an input whose own ndistinct is
// unknown all leave the column `unknown` and today's defaults.
func synthWindowOutputs(out *cteOutputStats, entry *plannedCTE) {
	win, indexMap, ok := windowOutputMap(peelCTEBody(entry.body))
	if !ok || win == nil || win.Child == nil {
		return
	}
	childWidth := len(win.Child.Output())
	for i := range out.cols {
		// Never overwrite a record an earlier rule already filled.
		if out.cols[i].kind != cteColUnknown {
			continue
		}
		wpos, ok := indexMap[i]
		if !ok || wpos < 0 || wpos >= childWidth {
			continue
		}
		nd := columnNDistinctForChild(wpos, win.Child)
		if nd <= 0 {
			continue
		}
		out.cols[i].kind = cteColPassthrough
		out.cols[i].ndistinct = float64(nd)
	}
}

// windowOutputMap is `aggOutputMap`'s twin for window bodies: it resolves a
// (possibly Project-wrapped) body to its `*WindowAgg` and the output-position
// map, result[i] = WindowAgg output position read by CTE output column i.
func windowOutputMap(body Node) (*WindowAgg, map[int]int, bool) {
	if win, ok := body.(*WindowAgg); ok && win != nil {
		m := make(map[int]int, len(win.Output()))
		for i := range win.Output() {
			m[i] = i
		}
		return win, m, true
	}
	proj, ok := body.(*Project)
	if !ok || proj == nil {
		return nil, nil, false
	}
	win, ok := proj.Child.(*WindowAgg)
	if !ok || win == nil {
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
	return win, m, true
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

// --- consumers (M0145-0009 slice 1) ------------------------------------
//
// G1 in the B-06 design: `resolveBaseColumn` recurses through a `*CTEScan`
// into the body and finds no `*Aggregate`/`*SetOp` arm, so an aggregate or
// set-op CTE output resolves to nothing, yields nil stats, and every
// consumer falls to `defaultNumDistinct`. The synthesis above already
// derives the right number for exactly those shapes; this is the path that
// lets a consumer see it.
//
// Scope of THIS slice: the ndistinct channel only
// (`columnNDistinctForChild`). The group-combo registry (G2) and the
// agg-output FD bound (G3) are separate consumers and stay unwired — they
// are slice 2, and wiring them blind would move default-arm plans on a
// mechanism this slice has not measured.

// cteSynthNDistinct resolves `idx` through the index-preserving wrappers
// down to a `*CTEScan` and returns that output column's synthesized
// ndistinct.
//
// The wrapper set is deliberately the same one `resolvesToGroupUniqueColumn`
// walks, and for the same reason: these are the nodes that do not change a
// column's identity, so an index may cross them unchanged. `*Project` is the
// one that remaps, and only a bare `*ColumnRef` target is followed — a
// computed target is a new value whose distinctness the synthesis says
// nothing about.
//
// Fail-closed at every step, per the task's standing rule: an unrecognised
// wrapper, an out-of-range index, an unpopulated entry, or a column the
// synthesis left `unknown` (ndistinct < 0) all return false, which leaves
// the caller on today's defaults. This function can only ever REPLACE a
// default with a derived number; it can never invent one where the
// synthesis declined.
func cteSynthNDistinct(idx int, child Node) (int64, bool) {
	switch x := child.(type) {
	case *Filter:
		return cteSynthNDistinct(idx, x.Child)
	case *Sort:
		return cteSynthNDistinct(idx, x.Child)
	case *Limit:
		return cteSynthNDistinct(idx, x.Child)
	case *LockRows:
		return cteSynthNDistinct(idx, x.Child)
	case *Gather:
		return cteSynthNDistinct(idx, x.Child)
	case *GatherMerge:
		return cteSynthNDistinct(idx, x.Child)
	case *Project:
		if idx >= 0 && idx < len(x.Targets) {
			if cr, ok := x.Targets[idx].(*ColumnRef); ok {
				return cteSynthNDistinct(cr.Index, x.Child)
			}
		}
	case *CTEScan:
		st := x.cte.outputStats()
		if st == nil || idx < 0 || idx >= len(st.cols) {
			return 0, false
		}
		col := st.cols[idx]
		if col.ndistinct < 0 {
			return 0, false
		}
		// Clamp to the body's own row estimate: a CTE cannot emit more
		// distinct values of a column than it emits rows. This is the
		// "clamped by output rows" half of the task's step-2 wording, and
		// it matters most for the group-key columns, whose combo-derived
		// ndistinct is an upper bound taken from the inputs rather than
		// from the grouped output.
		nd := col.ndistinct
		if st.rows > 0 && nd > st.rows {
			nd = st.rows
		}
		if nd < 1 {
			return 0, false
		}
		return saturateRowEst(nd), true
	}
	return 0, false
}
