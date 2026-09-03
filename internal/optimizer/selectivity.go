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
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/parser"

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
		case parser.OpLike, parser.OpILike, parser.OpNotLike, parser.OpNotILike:
			// patternsel (like_support.c) — take2 P1-14b. LIKE with no
			// estimator fell through to defaultGenericSelectivity (1/3);
			// TPC-H Q9's `part LIKE '%green%'` priced 66,666 vs ~10.6k.
			return patternClauseSelectivity(e.Op, e.Left, e.Right, child)
		}
	case *IsNullExpr:
		// nulltestsel (postgres/src/backend/utils/adt/selfuncs.c). take2 P1-14.
		//
		// ANALYZE has always collected NullFrac and persisted it as
		// stanullfrac, and this is the ONE clause it exists to answer — yet
		// `IS NULL` had no arm at all and fell through to the generic default
		// below, so the statistic was never read for its own predicate.
		return nullTestSelectivity(e, child)
	case *IsBoolExpr:
		// booltestsel (selfuncs.c:1545) — take2 P1-14b. IS TRUE/FALSE had
		// no arm at all and fell through to the generic default, so a
		// boolean column's statistics were never read for their own
		// predicate — the same defect P1-14 just closed for IS NULL.
		return boolTestSelectivity(e, child)
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
			sel += eqSelectivityForColumn(stats, v, columnRawRowsForChild(cr.Index, child))
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
	return eqSelectivityForColumn(stats, val, columnRawRowsForChild(col.Index, child))
}

// eqSelectivityForColumn prices `col = const`. `tuples` is the relation's RAW
// tuple count, needed only to resolve the relative ndistinct form; pass 0 when
// it is unknown and the absolute form will still be used.
func eqSelectivityForColumn(stats *catalog.ColumnStats, val Expr, tuples float64) float64 {
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
	// take2 P2-09: the RESOLVED ndistinct, not the raw absolute field. A
	// column whose distinct count scales with the relation stores the relative
	// form, and reading `.NDistinct` alone saw zero for it — so every equality
	// against a key column fell to defaultEqSelectivity and an IN-list over one
	// was out by three orders of magnitude.
	remainingDistinct := stats.ResolvedNDistinct(tuples) - float64(len(stats.MCV))
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
	return mass / remainingDistinct
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

	// take2 P1-11b: `convert_timevalue_to_scalar` (selfuncs.c). Date and
	// timestamp values become a scalar so histogram interpolation can measure
	// WHERE in a bucket the literal falls.
	//
	// Without this the whole date family fell to `bucketFraction`'s flat 0.5.
	// Measured on lineitem, `l_shipdate <` at three points:
	// -0.19 %, -0.99 %, -3.22 % error. So the bucket itself was already found
	// correctly — ISO-8601 strings sort in date order, which is why the error
	// is bounded by one bucket rather than unbounded — and this removes the
	// residual half-bucket. It is a fidelity fix of a few percent, not the
	// large win the item's original wording implied (07 §4 records the
	// correction).
	case "date":
		if t, err := time.Parse("2006-01-02", strings.TrimSpace(s)); err == nil {
			// Julian-style day number; only differences matter here.
			return float64(t.Unix()) / 86400.0, true
		}
		return 0, false
	case "timestamp", "timestamp without time zone", "timestamptz", "timestamp with time zone":
		for _, layout := range []string{
			"2006-01-02 15:04:05.999999-07",
			"2006-01-02 15:04:05.999999",
			"2006-01-02 15:04:05",
			"2006-01-02",
		} {
			if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
				return float64(t.UnixNano()) / 1e9, true
			}
		}
		return 0, false
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
// columnRawRowsForChild is columnStatsForChild's companion: the relation's RAW,
// unfiltered tuple count, which is the divisor the relative ndistinct form
// needs. Returns 0 when the column does not resolve to a base relation, which
// ResolvedNDistinct treats as "absolute form only". take2 P2-09.
func columnRawRowsForChild(idx int, child Node) float64 {
	if ref, ok := resolveBaseColumn(idx, child); ok {
		return ref.rawRows
	}
	return 0
}

func columnStatsForChild(idx int, child Node) *catalog.ColumnStats {
	// take2 P1-26: ONE arm list, not two.
	//
	// This used to be a second full walker over the plan tree, duplicating
	// resolveBaseColumn's arms — and its own comment recorded the rule it was
	// breaking: "kept in step with columnNDistinctForChild's arm list
	// (hard-won rule: sibling paths change together)". Keeping two walkers in
	// step by hand is what the sibling-paths rule exists to prevent, and the
	// pair had already drifted: this one had NO *IndexScan arm, so a column
	// reached through an index-probed leaf resolved to no statistics at all
	// and every clause over it fell to a default selectivity, while the
	// ndistinct twin resolved it fine.
	//
	// Delegating gives the index-probed leaf MCV and histogram access and
	// makes future drift impossible rather than merely discouraged.
	return columnStatsForChildBase(idx, child)
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
		case parser.OpLike, parser.OpILike, parser.OpNotLike, parser.OpNotILike:
			return patternClauseSelectivityWithSource(e.Op, e.Left, e.Right, child)
		}
	case *IsBoolExpr:
		if v, ok := boolTestSelectivityInner(e, child); ok {
			return selectivityEstimate{value: v, reliable: true}
		}
		return selectivityEstimate{value: defaultGenericSelectivity, reliable: false}
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
			sel += eqSelectivityForColumn(stats, v, columnRawRowsForChild(cr.Index, child))
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
	return selectivityEstimate{value: eqSelectivityForColumn(stats, val, columnRawRowsForChild(col.Index, child)), reliable: true}
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

// defaultUnkSel / defaultNotUnkSel are PG's DEFAULT_UNK_SEL and
// DEFAULT_NOT_UNK_SEL (postgres/src/include/utils/selfuncs.h:55-56), used when
// no statistics are available for the column under test.
const (
	defaultUnkSel    = 0.005
	defaultNotUnkSel = 1.0 - defaultUnkSel
)

// boolTestSelectivity estimates `x IS [NOT] TRUE/FALSE/UNKNOWN` from the
// column's statistics, mirroring booltestsel
// (postgres/src/backend/utils/adt/selfuncs.c:1545).
//
// A boolean column has at most two distinct values, so MCV[0] plus the null
// fraction determines everything: when MCV[0] is TRUE its frequency is the
// TRUE mass, otherwise the TRUE mass is 1 - freq(MCV[0]) - nullfrac, and the
// FALSE mass is whatever remains. Without MCV data the null fraction still
// answers UNKNOWN, and TRUE/FALSE split the non-null mass 50/50.
//
// Two deliberate deviations, both recorded: (1) with NO statistics at all
// the estimator declines to the pre-existing generic default rather than
// following PG's recurse-into-the-argument rule — goopg has no bare-boolean
// estimator for that recursion to land on, so PG's rule has no honest
// target here; (2) a non-column operand likewise keeps the old default, as
// does an MCV[0] whose rendered form is not recognisably boolean (which
// falls through to the no-MCV rule).
func boolTestSelectivity(e *IsBoolExpr, child Node) float64 {
	est, _ := boolTestSelectivityInner(e, child)
	return est
}

// boolTestKind maps the test flags onto PG's BoolTestType vocabulary.
func boolTestKind(e *IsBoolExpr) string {
	switch {
	case e.TestTrue && !e.TestFalse && !e.Negated:
		return "true"
	case e.TestTrue && !e.TestFalse && e.Negated:
		return "nottrue"
	case !e.TestTrue && e.TestFalse && !e.Negated:
		return "false"
	case !e.TestTrue && e.TestFalse && e.Negated:
		return "notfalse"
	case !e.TestTrue && !e.TestFalse && !e.Negated:
		return "unknown"
	default:
		return "notunknown"
	}
}

// isTrueMCVValue / isFalseMCVValue recognise a boolean MCV entry's
// rendered form. MCV values compare by rendered text throughout the
// estimator (take2 P1-15), so the match is textual: the full words in any
// case plus the single-letter/numeral abbreviations datasets use.
func isTrueMCVValue(v string) bool {
	switch strings.ToLower(v) {
	case "true", "t", "1":
		return true
	}
	return false
}

func isFalseMCVValue(v string) bool {
	switch strings.ToLower(v) {
	case "false", "f", "0":
		return true
	}
	return false
}

func boolTestSelectivityInner(e *IsBoolExpr, child Node) (float64, bool) {
	cr, ok := e.Operand.(*ColumnRef)
	if !ok {
		return defaultGenericSelectivity, false
	}
	cs := columnStatsForChild(cr.Index, child)
	if cs == nil {
		return defaultGenericSelectivity, false
	}
	freqNull := cs.NullFrac
	if freqNull < 0 {
		freqNull = 0
	}
	if freqNull > 1 {
		freqNull = 1
	}
	// MCV[0] decides the split when it is recognisably boolean; anything
	// else (including no MCV at all) uses the nullfrac-adjusted 50/50.
	freqTrue, freqFalse := -1.0, -1.0
	if len(cs.MCV) > 0 {
		if isTrueMCVValue(cs.MCV[0].Value) {
			freqTrue = cs.MCV[0].Frequency
		} else if isFalseMCVValue(cs.MCV[0].Value) {
			freqTrue = 1.0 - cs.MCV[0].Frequency - freqNull
		}
	}
	if freqTrue >= 0 {
		freqFalse = 1.0 - freqTrue - freqNull
	}
	var sel float64
	switch boolTestKind(e) {
	case "unknown":
		sel = freqNull
	case "notunknown":
		sel = 1.0 - freqNull
	case "true":
		if freqTrue >= 0 {
			sel = freqTrue
		} else {
			sel = (1.0 - freqNull) / 2.0
		}
	case "false":
		if freqTrue >= 0 {
			sel = freqFalse
		} else {
			sel = (1.0 - freqNull) / 2.0
		}
	case "nottrue":
		if freqTrue >= 0 {
			sel = 1.0 - freqTrue
		} else {
			sel = (freqNull + 1.0) / 2.0
		}
	case "notfalse":
		if freqTrue >= 0 {
			sel = 1.0 - freqFalse
		} else {
			sel = (freqNull + 1.0) / 2.0
		}
	}
	return clampProbability(sel), true
}

// nullTestSelectivity estimates `x IS NULL` / `x IS NOT NULL` from the column's
// recorded null fraction, mirroring nulltestsel.
//
// PG reads stats->stanullfrac and returns it directly for IS_NULL, or
// 1 - stanullfrac for IS NOT NULL; with no statistics it falls back to
// DEFAULT_UNK_SEL / DEFAULT_NOT_UNK_SEL.
func nullTestSelectivity(e *IsNullExpr, child Node) float64 {
	cr, ok := e.Operand.(*ColumnRef)
	if !ok {
		// Not a bare column — PG's nulltestsel handles only Var and Const
		// operands and defaults for anything else.
		if e.Negated {
			return defaultNotUnkSel
		}
		return defaultUnkSel
	}
	cs := columnStatsForChild(cr.Index, child)
	if cs == nil {
		if e.Negated {
			return defaultNotUnkSel
		}
		return defaultUnkSel
	}
	freqNull := cs.NullFrac
	if freqNull < 0 {
		freqNull = 0
	}
	if freqNull > 1 {
		freqNull = 1
	}
	if e.Negated {
		return 1.0 - freqNull
	}
	return freqNull
}
