package optimizer

import (
	"strconv"

	"github.com/goopg/goopg/internal/parser"
)

// Range-clause pairing — PostgreSQL's RangeQueryClause handling from
// clauselist_selectivity (postgres/src/backend/optimizer/path/clausesel.c).
//
// WHY. An AND of two inequalities on the SAME column is not two independent
// events. `l_shipdate >= '1994-01-01' AND l_shipdate < '1995-01-01'` selects one
// year from a column spanning about seven, i.e. ~0.14. Estimated as independent
// factors it comes out at
//
//	P(>= 1994) * P(< 1995)  ~=  0.714 * 0.428  =  0.306
//
// — a 2.1x overestimate, and the shape is everywhere in both benchmark suites
// (TPC-H Q1, Q3, Q4, Q5, Q6, Q7, Q8, Q10, Q12, Q14, Q15, Q20).
//
// PG's rule: for `x > a` the selectivity is the fraction ABOVE a, for `x < b`
// the fraction BELOW b, so when both bounds are present the fraction BETWEEN
// them is `hibound + lobound - 1`. clausesel.c:273 onward.
//
// take2 P1-13. This only became worth doing once histograms survived a restart
// (P1-11c): before that both bounds returned DEFAULT_INEQ_SEL and the pairing
// had nothing real to combine.

// defaultRangeIneqSel is PG's DEFAULT_RANGE_INEQ_SEL
// (postgres/src/include/utils/selfuncs.h:40).
const defaultRangeIneqSel = 0.005

// rangeQueryClause groups the inequality bounds found on one variable, mirroring
// clausesel.c's RangeQueryClause struct.
type rangeQueryClause struct {
	key         string
	haveLoBound bool
	haveHiBound bool
	loBound     float64
	hiBound     float64
}

// splitConjuncts flattens an AND tree into its conjuncts, as
// clauselist_selectivity receives an already-flattened implicit-AND list.
func splitConjuncts(e Expr, out []Expr) []Expr {
	if b, ok := e.(*BinaryOp); ok && b.Op == parser.OpAnd {
		out = splitConjuncts(b.Left, out)
		return splitConjuncts(b.Right, out)
	}
	return append(out, e)
}

// rangeBoundOf reports whether e is `var <op> const` (or the mirrored
// `const <op> var`) with an inequality operator, and if so returns a stable key
// for the variable and whether the clause is a LOW bound.
//
// PG decides lo/hi from the operator's strategy number after normalising which
// side the Var is on (clausesel.c:236-251): `x > c` and `c < x` are both low
// bounds. The same normalisation is done here through normalizeColumnConstRange,
// which reports whether the column was on the right so the operator can be
// flipped.
func rangeBoundOf(e Expr) (key string, isLo bool, ok bool) {
	b, isBin := e.(*BinaryOp)
	if !isBin {
		return "", false, false
	}
	switch b.Op {
	case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
	default:
		return "", false, false
	}
	cr, _, colOnRight, matched := normalizeColumnConstRange(b.Left, b.Right)
	if !matched || cr == nil {
		return "", false, false
	}
	op := b.Op
	if colOnRight {
		op = flipComparison(op)
	}
	// After normalisation the column is on the left, so `>`/`>=` bound it from
	// below and `<`/`<=` from above.
	switch op {
	case parser.OpGt, parser.OpGe:
		return rangeVarKey(cr), true, true
	default:
		return rangeVarKey(cr), false, true
	}
}

// flipComparison mirrors an operator across its operands: `c < x` means the
// same as `x > c`.
func flipComparison(op parser.OpCode) parser.OpCode {
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

// rangeVarKey identifies the variable a bound applies to. PG compares Vars with
// equal(); goopg's ColumnRef carries the resolved binding coordinate, so the
// FROM-item index plus the column index is the same identity. The resolved
// name is not sufficient on its own — two relations in one query can both have
// a column called `id`, and pairing their bounds would be wrong.
func rangeVarKey(cr *ColumnRef) string {
	return strconv.Itoa(int(cr.SourceTableIdx)) + "\x00" + strconv.Itoa(cr.Index)
}

// conjunctionSelectivity is goopg's clauselist_selectivity: it estimates an
// AND-list, pairing inequality bounds on the same variable instead of
// multiplying them as independent events.
func conjunctionSelectivity(conjuncts []Expr, child Node) float64 {
	var groups []*rangeQueryClause
	find := func(key string) *rangeQueryClause {
		for _, g := range groups {
			if g.key == key {
				return g
			}
		}
		g := &rangeQueryClause{key: key}
		groups = append(groups, g)
		return g
	}

	s1 := 1.0
	for _, c := range conjuncts {
		key, isLo, ok := rangeBoundOf(c)
		if !ok {
			// Not a pairable bound: multiply it in as before.
			s1 *= clauseSelectivity(c, child)
			continue
		}
		s2 := clauseSelectivity(c, child)
		g := find(key)
		if isLo {
			// Two similar clauses (`x > y AND x >= z`): keep the more
			// restrictive one, as clausesel.c:456-471 does.
			if !g.haveLoBound || g.loBound > s2 {
				g.haveLoBound, g.loBound = true, s2
			}
		} else {
			if !g.haveHiBound || g.hiBound > s2 {
				g.haveHiBound, g.hiBound = true, s2
			}
		}
	}

	for _, g := range groups {
		if !g.haveLoBound || !g.haveHiBound {
			// Only one side: it is an ordinary inequality and contributes its
			// own selectivity (clausesel.c:330-340).
			if g.haveLoBound {
				s1 *= g.loBound
			}
			if g.haveHiBound {
				s1 *= g.hiBound
			}
			continue
		}
		s2 := g.hiBound + g.loBound - 1.0
		// PG additionally adds nulltestsel(IS_NULL) here to undo the
		// double-exclusion of NULLs. goopg has no nulltestsel yet (P1-14), so
		// that term is omitted — it only matters for a column with a
		// significant null fraction, and omitting it UNDER-estimates slightly
		// rather than reverting to the independent product.
		switch {
		case s2 < -0.01:
			// Very negative means at least one side was a default estimate we
			// failed to recognise; PG falls back rather than trusting it.
			s2 = defaultRangeIneqSel
		case s2 <= 0.0:
			// Just roundoff on a very tight range.
			s2 = 1.0e-10
		}
		s1 *= s2
	}
	if s1 < 0 {
		return 0
	}
	if s1 > 1 {
		return 1
	}
	return s1
}
