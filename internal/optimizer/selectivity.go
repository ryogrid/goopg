// Predicate selectivity for the Filter case of EstimateRows.
//
// `clauseSelectivity` decomposes a Filter's predicate the same way
// upstream's `clauselist_selectivity` (postgres/src/backend/utils/
// adt/selfuncs.c) does, and returns a value in [0, 1] that scales
// the child estimate. AND multiplies, OR uses inclusion-exclusion,
// equality probes the MCV list with a non-MCV fallback, and range
// predicates interpolate the equi-depth histogram.
//
// When stats aren't available — table never ANALYZEd, predicate
// shape unrecognised, ColumnRef on neither side — we fall back to
// `defaultGenericSelectivity` (1/3). That keeps the M0003
// rules-only behaviour as the documented fallback.
//
// See docs/design/0006-0003-clauselist-selectivity.md.
package optimizer

import (
	"github.com/goopg/goopg/internal/parser"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// clauseSelectivity returns the fraction of `child`'s rows expected
// to pass `expr`. Always in [0, 1].
func clauseSelectivity(expr Expr, child Node) float64 {
	if expr == nil {
		return 1.0
	}
	switch e := expr.(type) {
	case *BinaryOp:
		switch e.Op {
		case parser.OpAnd:
			// Independence is upstream's default for UNRELATED clauses, but
			// two inequalities on the SAME variable are not independent:
			// `x >= a AND x < b` selects the band between them, not the
			// product of two tail fractions. clauselist_selectivity pairs them
			// (clausesel.c); conjunctionSelectivity is that pairing, and it
			// falls back to the independent product for everything else.
			// take2 P1-13.
			return conjunctionSelectivity(splitConjuncts(e, nil), child)
		case parser.OpOr:
			a := clauseSelectivity(e.Left, child)
			b := clauseSelectivity(e.Right, child)
			return a + b - a*b
		case parser.OpEq:
			return eqOpSelectivity(e.Left, e.Right, child)
		case parser.OpNe:
			eq := eqOpSelectivity(e.Left, e.Right, child)
			return 1 - eq
		case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
			return rangeOpSelectivity(e.Op, e.Left, e.Right, child)
		}
	case *UnaryOp:
		if e.Op == parser.OpNot {
			return 1 - clauseSelectivity(e.Operand, child)
		}
	case *InExpr:
		// Only the value-list form matches the column-IN-(consts)
		// shape selfuncs handles. The subquery form is handled by
		// the join / subplan estimator.
		if e.Plan != nil || len(e.List) == 0 {
			return defaultGenericSelectivity
		}
		cr, ok := e.Operand.(*ColumnRef)
		if !ok {
			return defaultGenericSelectivity
		}
		stats := columnStatsForChild(cr.Index, child)
		var sel float64
		for _, v := range e.List {
			sel += eqSelectivityForColumn(stats, v)
		}
		if sel > 1.0 {
			sel = 1.0
		}
		if e.Negated {
			return 1 - sel
		}
		return sel
	case *BooleanConst:
		if e.Value {
			return 1.0
		}
		return 0.0
	}
	return defaultGenericSelectivity
}

// eqOpSelectivity handles `col = const` (or the swapped `const =
// col`). Returns the MCV frequency on a hit, the non-MCV fallback,
// or — when stats are missing — the upstream `1/200` constant.
func eqOpSelectivity(left, right Expr, child Node) float64 {
	col, val, ok := normalizeColumnConst(left, right)
	if !ok {
		return defaultEqSelectivity
	}
	stats := columnStatsForChild(col.Index, child)
	return eqSelectivityForColumn(stats, val)
}

func eqSelectivityForColumn(stats *catalog.ColumnStats, val Expr) float64 {
	literal, ok := formatExprConstant(val)
	if !ok {
		return defaultEqSelectivity
	}
	if stats == nil {
		return defaultEqSelectivity
	}
	for _, mcv := range stats.MCV {
		if mcv.Value == literal {
			return mcv.Frequency
		}
	}
	// Non-MCV bucket: split the leftover mass across the
	// non-MCV distinct values.
	mcvMass := 0.0
	for _, mcv := range stats.MCV {
		mcvMass += mcv.Frequency
	}
	remainingDistinct := stats.NDistinct - int64(len(stats.MCV))
	if remainingDistinct <= 0 {
		// MCV covers every distinct value — the constant isn't
		// among them. Selectivity is the residual mass /
		// nullFrac-adjusted; conservatively report the upstream
		// default fallback.
		return defaultEqSelectivity
	}
	mass := 1.0 - mcvMass - stats.NullFrac
	if mass <= 0 {
		return defaultEqSelectivity
	}
	return mass / float64(remainingDistinct)
}

// rangeOpSelectivity handles `col <op> const` for `< <= > >=`.
// Returns the histogram-bucket interpolation, scaled to the
// non-MCV mass, plus contributions from MCV values that satisfy
// the predicate.
func rangeOpSelectivity(op parser.OpCode, left, right Expr, child Node) float64 {
	col, val, swapped, ok := normalizeColumnConstRange(left, right)
	if !ok {
		return defaultIneqSelectivity
	}
	if swapped {
		op = swapInequalityOp(op)
	}
	stats := columnStatsForChild(col.Index, child)
	if stats == nil || len(stats.Histogram) < 2 {
		return defaultIneqSelectivity
	}
	literal, ok := formatExprConstant(val)
	if !ok {
		return defaultIneqSelectivity
	}

	// Histogram covers the non-MCV mass.
	mcvMass := 0.0
	mcvHits := 0.0
	for _, mcv := range stats.MCV {
		mcvMass += mcv.Frequency
		if histCmp(mcv.Value, literal, col.Type.Name) >= 0 {
			// Need to know if this MCV value satisfies op.
			c := histCmp(mcv.Value, literal, col.Type.Name)
			if rangeOpMatches(op, c) {
				mcvHits += mcv.Frequency
			}
		} else {
			c := -1
			if rangeOpMatches(op, c) {
				mcvHits += mcv.Frequency
			}
		}
	}
	nonMCVMass := 1.0 - mcvMass - stats.NullFrac
	if nonMCVMass < 0 {
		nonMCVMass = 0
	}
	histSel := histogramOpSelectivity(op, stats.Histogram, literal, col.Type.Name)
	sel := mcvHits + histSel*nonMCVMass
	if sel < 0 {
		return 0
	}
	if sel > 1 {
		return 1
	}
	return sel
}

// histogramOpSelectivity returns the fraction of the histogram's
// mass (treated as 1.0 across the boundaries) that satisfies op
// for the given literal. Boundaries are sorted ascending.
func histogramOpSelectivity(op parser.OpCode, bounds []string, literal, typeName string) float64 {
	k := len(bounds) - 1 // bucket count
	if k < 1 {
		return defaultIneqSelectivity
	}
	// Find the first boundary >= literal.
	idx := -1
	for i, b := range bounds {
		if histCmp(b, literal, typeName) >= 0 {
			idx = i
			break
		}
	}
	switch op {
	case parser.OpLt, parser.OpLe:
		if idx <= 0 {
			// literal <= bounds[0]: nothing to the left.
			if idx == 0 && op == parser.OpLe && histCmp(bounds[0], literal, typeName) == 0 {
				// literal == low boundary; <= keeps a sliver of
				// the first bucket. Approximate as 1/k.
				return 1.0 / float64(k)
			}
			return 0.0
		}
		if idx == -1 {
			// literal greater than every boundary.
			return 1.0
		}
		whole := float64(idx-1) / float64(k)
		frac := bucketFraction(bounds[idx-1], bounds[idx], literal, typeName)
		return whole + frac/float64(k)
	case parser.OpGt, parser.OpGe:
		// Symmetric: 1 - sel(<) for >=, 1 - sel(<=) for >.
		flip := parser.OpLe
		if op == parser.OpGe {
			flip = parser.OpLt
		}
		return 1.0 - histogramOpSelectivity(flip, bounds, literal, typeName)
	}
	return defaultIneqSelectivity
}

// bucketFraction returns the fraction of a single histogram
// bucket [lo, hi] that lies below `lit`. Always in [0, 1].
func bucketFraction(lo, hi, lit, typeName string) float64 {
	loN, loOK := numericValue(lo, typeName)
	hiN, hiOK := numericValue(hi, typeName)
	litN, litOK := numericValue(lit, typeName)
	if !loOK || !hiOK || !litOK || hiN <= loN {
		// Non-numeric or zero-width bucket: best-effort half.
		return 0.5
	}
	if litN <= loN {
		return 0
	}
	if litN >= hiN {
		return 1
	}
	return (litN - loN) / (hiN - loN)
}

// rangeOpMatches reports whether `cmp(a,b) <op> 0` holds for the
// given op. cmp is -1/0/1 in the usual sense.
func rangeOpMatches(op parser.OpCode, cmp int) bool {
	switch op {
	case parser.OpLt:
		return cmp < 0
	case parser.OpLe:
		return cmp <= 0
	case parser.OpGt:
		return cmp > 0
	case parser.OpGe:
		return cmp >= 0
	}
	return false
}

// histCmp compares two histogram-bound-formatted strings under
// the per-type total order. Integer / numeric / time go through
// numericValue; strings fall back to byte-wise compare.
func histCmp(a, b, typeName string) int {
	if an, ok := numericValue(a, typeName); ok {
		if bn, ok := numericValue(b, typeName); ok {
			switch {
			case an < bn:
				return -1
			case an > bn:
				return 1
			}
			return 0
		}
	}
	return strings.Compare(a, b)
}

// numericValue parses a histogram-bound or literal string back to
// a float64 for numeric / integer / numeric-typed columns. Reports
// (value, true) when the type is numeric and parsing succeeds;
// otherwise (0, false). String / bool columns cannot use this
// path and fall back to byte-wise compare in histCmp.
func numericValue(s, typeName string) (float64, bool) {
	switch strings.ToLower(typeName) {
	case "int", "int2", "int4", "int8", "smallint", "integer", "bigint", "numeric", "decimal", "float", "real", "double precision":
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// normalizeColumnConst recognises `col op const` and `const op col`
// for equality. Returns the column ref and the constant side; ok
// is false when the predicate isn't shaped that way.
func normalizeColumnConst(l, r Expr) (*ColumnRef, Expr, bool) {
	if cr, ok := l.(*ColumnRef); ok && isConstExpr(r) {
		return cr, r, true
	}
	if cr, ok := r.(*ColumnRef); ok && isConstExpr(l) {
		return cr, l, true
	}
	return nil, nil, false
}

// normalizeColumnConstRange is the same shape recogniser as
// normalizeColumnConst, but it also reports whether the column
// was on the right (so callers can flip the operator).
func normalizeColumnConstRange(l, r Expr) (*ColumnRef, Expr, bool, bool) {
	if cr, ok := l.(*ColumnRef); ok && isConstExpr(r) {
		return cr, r, false, true
	}
	if cr, ok := r.(*ColumnRef); ok && isConstExpr(l) {
		return cr, l, true, true
	}
	return nil, nil, false, false
}

func swapInequalityOp(op parser.OpCode) parser.OpCode {
	switch op {
	case parser.OpLt:
		return parser.OpGt
	case parser.OpLe:
		return parser.OpGe
	case parser.OpGt:
		return parser.OpLt
	case parser.OpGe:
		return parser.OpLe
	}
	return op
}

// isConstExpr reports whether e is a literal the planner can
// render through formatExprConstant for MCV / histogram lookup.
// ParamRef is intentionally excluded — bind values aren't known
// at plan time.
func isConstExpr(e Expr) bool {
	switch e.(type) {
	case *IntegerConst, *StringConst, *NumericConst, *BooleanConst, *TypedStringLit:
		return true
	}
	return false
}

// formatExprConstant renders a constant expression to the same
// canonical string ANALYZE used when stamping MCV.Value /
// Histogram entries. The match is byte-equal — no smart numeric
// canonicalisation is needed because both sides go through the
// executor's Datum.Format and the planner's formatter mirrors it.
func formatExprConstant(e Expr) (string, bool) {
	switch x := e.(type) {
	case *IntegerConst:
		return strconv.FormatInt(x.Value, 10), true
	case *StringConst:
		return x.Value, true
	case *NumericConst:
		return x.Value, true
	case *BooleanConst:
		if x.Value {
			return "t", true
		}
		return "f", true
	case *TypedStringLit:
		// Date / timestamp literals share the value byte-for-byte
		// with what ANALYZE stamps via Datum.Format for those
		// kinds — both render through Go's time formatting.
		return x.Value, true
	}
	return "", false
}

// columnStatsForChild walks the child plan tree to find a
// SeqScan whose Table.Stats describes the column at logical
// index `idx` (matches the executor's Output indexing). Returns
// nil when the column doesn't trace back to a base relation
// with stats — that's the unanalysed-table fallback.
func columnStatsForChild(idx int, child Node) *catalog.ColumnStats {
	switch x := child.(type) {
	case *SeqScan:
		if x.Table == nil || x.Table.Stats == nil {
			return nil
		}
		if idx < 0 || idx >= len(x.Table.Stats.Columns) {
			return nil
		}
		return &x.Table.Stats.Columns[idx]
	case *Filter:
		return columnStatsForChild(idx, x.Child)
	case *Sort:
		return columnStatsForChild(idx, x.Child)

	// M0127-P5.6-e-ii: the Project arm used to pass `idx` straight
	// through, which is only right when the target list is the
	// identity — its ndistinct twin has remapped through `Targets`
	// since M0125-0038. A reordering or narrowing Project therefore
	// returned ANOTHER column's MCV list and histogram, which is worse
	// than returning none. The divergence became reachable far more
	// often with the *Join arm below, since a join input is routinely
	// Project-wrapped.
	case *Project:
		if idx >= 0 && idx < len(x.Targets) {
			if cr, ok := x.Targets[idx].(*ColumnRef); ok {
				return columnStatsForChild(cr.Index, x.Child)
			}
		}
		return nil

	// The remaining pass-through wrappers, kept in step with
	// `columnNDistinctForChild`'s arm list (hard-won rule: sibling
	// paths change together). Each preserves its child's schema
	// position for position.
	case *Limit:
		return columnStatsForChild(idx, x.Child)
	case *LockRows:
		return columnStatsForChild(idx, x.Child)
	case *Gather:
		return columnStatsForChild(idx, x.Child)
	case *GatherMerge:
		return columnStatsForChild(idx, x.Child)
	case *CTEScan:
		return columnStatsForChild(idx, x.Child)

	// M0127-P5.6-e-ii: the *Join twin of `columnNDistinctForChild`'s
	// arm — see the long comment there for the coordinate rule. Without
	// it a join-level restriction (Q19's three-branch OR over `part` and
	// `lineitem`) resolved no stats at all and every one of its branches
	// collapsed to the `defaultEq`/`defaultIneq` constants.
	case *Join:
		if x.Left == nil || x.Right == nil {
			return nil
		}
		lw := len(x.Left.Output())
		if lw == 0 {
			return nil
		}
		if idx >= lw {
			return columnStatsForChild(idx-lw, x.Right)
		}
		return columnStatsForChild(idx, x.Left)
	}
	return nil
}

// selectivityEstimate carries a clause's selectivity together
// with a `reliable` flag that distinguishes stat-driven results
// from generic fallbacks. (M0077-0002 / Slice B per design 02
// §2.) Used by `estimateBaseRelInfo` so a relation's filtered
// row count is only updated from the scaled value when the
// estimate is trustworthy.
type selectivityEstimate struct {
	value    float64
	reliable bool
}

// clauseSelectivityWithSource is the reliability-tracking twin
// of `clauseSelectivity`. It returns the same numeric estimate
// but also reports whether real column stats drove the result.
// AND/OR composition is reliable iff both children are; UnaryOp
// NOT inherits its operand's flag. Equality and range fallbacks
// (`defaultEqSelectivity`, `defaultIneqSelectivity`) and the
// generic `defaultGenericSelectivity` are reported as
// reliable=false. BooleanConst is exact (reliable=true).
//
// Slice B uses this to gate updates to
// `baseRelInfo.filteredRows`: when reliability is false, the
// row count keeps its pre-filter value rather than picking up
// arbitrary fallback constants that the cost model would then
// over-trust.
func clauseSelectivityWithSource(expr Expr, child Node) selectivityEstimate {
	if expr == nil {
		return selectivityEstimate{value: 1.0, reliable: true}
	}
	switch e := expr.(type) {
	case *BinaryOp:
		switch e.Op {
		case parser.OpAnd:
			a := clauseSelectivityWithSource(e.Left, child)
			b := clauseSelectivityWithSource(e.Right, child)
			return selectivityEstimate{value: a.value * b.value, reliable: a.reliable && b.reliable}
		case parser.OpOr:
			a := clauseSelectivityWithSource(e.Left, child)
			b := clauseSelectivityWithSource(e.Right, child)
			return selectivityEstimate{value: a.value + b.value - a.value*b.value, reliable: a.reliable && b.reliable}
		case parser.OpEq:
			return eqOpSelectivityWithSource(e.Left, e.Right, child)
		case parser.OpNe:
			eq := eqOpSelectivityWithSource(e.Left, e.Right, child)
			return selectivityEstimate{value: 1 - eq.value, reliable: eq.reliable}
		case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
			return rangeOpSelectivityWithSource(e.Op, e.Left, e.Right, child)
		}
	case *UnaryOp:
		if e.Op == parser.OpNot {
			sub := clauseSelectivityWithSource(e.Operand, child)
			return selectivityEstimate{value: 1 - sub.value, reliable: sub.reliable}
		}
	case *InExpr:
		if e.Plan != nil || len(e.List) == 0 {
			return selectivityEstimate{value: defaultGenericSelectivity, reliable: false}
		}
		cr, ok := e.Operand.(*ColumnRef)
		if !ok {
			return selectivityEstimate{value: defaultGenericSelectivity, reliable: false}
		}
		stats := columnStatsForChild(cr.Index, child)
		if stats == nil {
			return selectivityEstimate{value: defaultGenericSelectivity, reliable: false}
		}
		var sel float64
		for _, v := range e.List {
			sel += eqSelectivityForColumn(stats, v)
		}
		if sel > 1.0 {
			sel = 1.0
		}
		if e.Negated {
			return selectivityEstimate{value: 1 - sel, reliable: true}
		}
		return selectivityEstimate{value: sel, reliable: true}
	case *BooleanConst:
		if e.Value {
			return selectivityEstimate{value: 1.0, reliable: true}
		}
		return selectivityEstimate{value: 0.0, reliable: true}
	}
	return selectivityEstimate{value: defaultGenericSelectivity, reliable: false}
}

// eqOpSelectivityWithSource is the reliability-tracking twin of
// `eqOpSelectivity`. Reliable iff a `column = const` shape is
// matched AND the column has stats.
func eqOpSelectivityWithSource(left, right Expr, child Node) selectivityEstimate {
	col, val, ok := normalizeColumnConst(left, right)
	if !ok {
		return selectivityEstimate{value: defaultEqSelectivity, reliable: false}
	}
	stats := columnStatsForChild(col.Index, child)
	if stats == nil {
		return selectivityEstimate{value: defaultEqSelectivity, reliable: false}
	}
	return selectivityEstimate{value: eqSelectivityForColumn(stats, val), reliable: true}
}

// rangeOpSelectivityWithSource is the reliability-tracking twin
// of `rangeOpSelectivity`. Reliable iff a `column <op> const`
// shape is matched AND the column has a usable histogram.
// Generic fallback (no histogram, ≥2 entries required) reports
// reliable=false so `baseRelInfo.filteredRows` falls back to the
// pre-filter row count.
func rangeOpSelectivityWithSource(op parser.OpCode, left, right Expr, child Node) selectivityEstimate {
	col, _, _, ok := normalizeColumnConstRange(left, right)
	if !ok {
		return selectivityEstimate{value: defaultIneqSelectivity, reliable: false}
	}
	stats := columnStatsForChild(col.Index, child)
	if stats == nil || len(stats.Histogram) < 2 {
		return selectivityEstimate{value: defaultIneqSelectivity, reliable: false}
	}
	// Histogram present → trust rangeOpSelectivity's interpolation.
	val := rangeOpSelectivity(op, left, right, child)
	return selectivityEstimate{value: val, reliable: true}
}
