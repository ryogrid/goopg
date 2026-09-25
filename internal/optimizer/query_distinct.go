package optimizer

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/parser/analyzer"
)

// queryIsDistinctForFirstColumn is PG's `query_supports_distinctness` +
// `query_is_distinct_for` (postgres/src/backend/optimizer/plan/
// analyzejoins.c:1079, :1117) specialised to the one question
// `create_unique_path` asks of a pulled-up ANY_subquery
// (postgres/src/backend/optimizer/util/pathnode.c:1955-1975): can the
// sub-select return two rows with the same value in its FIRST output column?
//
// The caller (M0145-0008ab) only asks it of a body with exactly one output
// column, whose one semi_rhs_expr is that column — so `colnos` is {1} and
// "every distinct/group column appears in colnos" becomes "every
// distinct/group expression IS the first target". The expression match is
// conservative: a positional reference (`GROUP BY 1`) or a qualifier-aware
// structural match of the target expression. An output-alias match is NOT
// taken, because PG resolves a bare GROUP BY name to an input column before an
// output alias (`findTargetlistEntrySQL92`), so `SELECT a AS k … GROUP BY k`
// may group by a different `k`. A false "no" only costs the free unique path;
// a false "yes" would make the inner join return duplicates.
//
// Operator compatibility (`equality_ops_are_compatible`) is not checked: the
// link is the plain `=` the sub-select's own grouping uses.
func queryIsDistinctForFirstColumn(s *parser.SelectStmt, cat catalog.Catalog) bool {
	if s == nil || len(s.Targets) == 0 {
		return false
	}
	// A grouping node `( operand ) …` carries no clauses of its own; the
	// answer for the pair is not the operand's alone. Refuse.
	if s.SetOpOperand != nil {
		return false
	}
	// UNION / INTERSECT / EXCEPT without ALL make the whole output row
	// distinct. goopg chains set operations through SetOp.Right, so the
	// top operation is not simply the first clause; requiring EVERY clause
	// in the tree to be non-ALL makes whichever one is on top non-ALL.
	// Checked before DISTINCT because `s.Distinct` describes only the first
	// operand of a chain.
	if s.SetOp != nil {
		return setOpTreeAllDistinct(s)
	}
	target := s.Targets[0].Expr
	if target == nil {
		return false
	}
	if _, isStar := target.(*parser.StarExpr); isStar {
		return false
	}
	// DISTINCT guarantees uniqueness even with SRFs in the target list.
	if s.Distinct && len(s.DistinctOn) == 0 {
		return len(s.Targets) == 1
	}
	if len(s.DistinctOn) > 0 {
		all := true
		for _, e := range s.DistinctOn {
			if !isFirstTargetExpr(e, s.Targets) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	// `query->hasTargetSRFs`: an SRF can return duplicates despite grouping.
	for _, t := range s.Targets {
		if t.Expr != nil && analyzer.ExprHasSRF(t.Expr, cat) {
			return false
		}
	}
	if s.GroupingSets != nil {
		// PG punts on grouping sets with expressions; the lone-empty-set
		// case it accepts has no GROUP BY expression goopg could hold here.
		return false
	}
	if len(s.GroupBy) > 0 {
		for _, g := range s.GroupBy {
			if !isFirstTargetExpr(g, s.Targets) {
				return false
			}
		}
		return true
	}
	// No GROUP BY but aggregates or HAVING: at most one row.
	if s.Having != nil {
		return true
	}
	for _, t := range s.Targets {
		if t.Expr != nil && exprHasAggregate(t.Expr) {
			return true
		}
	}
	return false
}

// isFirstTargetExpr reports whether a DISTINCT ON / GROUP BY item names the
// first target: positionally (`1`) or by a qualifier-aware structural match.
func isFirstTargetExpr(e parser.Expr, targets []parser.ResTarget) bool {
	if ic, ok := e.(*parser.IntegerConst); ok {
		return ic.Value == 1
	}
	return qualifiedGroupKey(e) == qualifiedGroupKey(targets[0].Expr)
}

// setOpTreeAllDistinct reports whether every set operation reachable from s —
// its own chain and every nested grouping operand — is a non-ALL one.
func setOpTreeAllDistinct(s *parser.SelectStmt) bool {
	if s == nil {
		return true
	}
	if s.SetOpOperand != nil && !setOpTreeAllDistinct(s.SetOpOperand) {
		return false
	}
	for c := s.SetOp; c != nil; {
		if c.All {
			return false
		}
		if c.Right == nil {
			return true
		}
		if c.Right.SetOpOperand != nil && !setOpTreeAllDistinct(c.Right.SetOpOperand) {
			return false
		}
		c = c.Right.SetOp
	}
	return true
}

// subqueryOutputIsUnique is the RTE_SUBQUERY arm of PG's
// `examine_simple_variable` (postgres/src/backend/utils/adt/selfuncs.c) for a
// sub-select's FIRST output column: set operations and grouping sets punt; a
// DISTINCT list of exactly one item naming the column, or else a GROUP BY list
// of exactly one item naming it, makes the column `isunique`. It is narrower
// than `queryIsDistinctForFirstColumn` on purpose — upstream's estimator and
// its unique-path test are two different rules, and the estimate must follow
// the estimator's (a HAVING-only aggregate body is distinct, yet not isunique).
func subqueryOutputIsUnique(s *parser.SelectStmt) bool {
	if s == nil || len(s.Targets) == 0 || s.Targets[0].Expr == nil {
		return false
	}
	if s.SetOpOperand != nil || s.SetOp != nil || s.GroupingSets != nil {
		return false
	}
	if _, isStar := s.Targets[0].Expr.(*parser.StarExpr); isStar {
		return false
	}
	// PG's distinctClause for plain DISTINCT lists every output column.
	if s.Distinct && len(s.DistinctOn) == 0 {
		return len(s.Targets) == 1
	}
	if len(s.DistinctOn) > 0 {
		return len(s.DistinctOn) == 1 && isFirstTargetExpr(s.DistinctOn[0], s.Targets)
	}
	if len(s.GroupBy) > 0 {
		return len(s.GroupBy) == 1 && isFirstTargetExpr(s.GroupBy[0], s.Targets)
	}
	return false
}
