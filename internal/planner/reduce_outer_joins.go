package planner

import "github.com/goopg/goopg/internal/parser"

// reduceOuterJoins demotes outer joins to inner joins when a strict qual above
// them constrains the nullable side. It is the reduction half of PG's
// reduce_outer_joins (postgres/src/backend/optimizer/prep/prepjointree.c).
//
// The RIGHT→LEFT flip half is unrepresentable in parser.FromExpr (Base RangeVar
// + flat []JoinExpr) and stays out — a ledger row records the split.
//
// Algorithm (simplified from PG's recursive two-pass by the flat join chain):
//  1. Walk the unresolved WHERE clause to collect table names/aliases in
//     strict-operator positions — these rels are forced non-null if the
//     WHERE passes.
//  2. For each FromExpr, walk its flat join chain. At each LEFT/RIGHT/FULL
//     join, check whether the nullable side's table name is in the
//     nonnullable set. If so, demote the join type.
//
// It modifies from in-place: JoinExpr.Type is changed when demotion fires.
// This is a pessimisation fix — it never produces a wrong answer, only
// opportunities it misses for lack of strictness information.
func reduceOuterJoins(from []parser.FromExpr, where parser.Expr) {
	if where == nil {
		return
	}
	nonnullable := collectNonNullableTableNames(where)
	if len(nonnullable) == 0 {
		return
	}
	for i := range from {
		applyDemotion(&from[i], nonnullable)
	}
}

// applyDemotion walks one FromExpr's flat join chain and demotes outer joins
// whose nullable side is constrained by a strict qual in nonnullable.
//
// The join chain is strictly left-deep: (((Base ⋈ J0.Right) ⋈ J1.Right) ⋈ …).
// For a LEFT JOIN at position i, the right (nullable) side is Joins[i].Right
// (a single RangeVar). For a RIGHT JOIN, the left (accumulated) side is
// nullable and we check whether ANY of its tables are in nonnullable. For a
// FULL JOIN, each side is checked independently.
func applyDemotion(item *parser.FromExpr, nonnullable map[string]bool) {
	// Accumulated set of table names on the left.
	leftNames := rangeVarNames(item.Base)

	for i := range item.Joins {
		j := &item.Joins[i]
		rightName := rangeVarPrimaryName(j.Right)

		switch j.Type {
		case parser.JoinLeft:
			// Left side is preserved, right side is nullable.
			if nonnullable[rightName] {
				j.Type = parser.JoinInner
			}

		case parser.JoinRight:
			// Right side is preserved, left (accumulated) side is nullable.
			if anyNameIn(leftNames, nonnullable) {
				j.Type = parser.JoinInner
			}

		case parser.JoinFull:
			// Both sides are nullable. Check each independently.
			leftConstrained := anyNameIn(leftNames, nonnullable)
			rightConstrained := nonnullable[rightName]
			if leftConstrained && rightConstrained {
				j.Type = parser.JoinInner
			} else if rightConstrained {
				// Right constrained → left preserved → becomes LEFT.
				j.Type = parser.JoinLeft
			}
			// Left-only constrained: would become RIGHT, but the flip
			// is unrepresentable — leave as FULL. (Ledger.)
		}

		// Accumulate right side's names for the next iteration.
		leftNames = append(leftNames, rangeVarNames(j.Right)...)
	}
}

// collectNonNullableTableNames walks a parser WHERE expression and returns the
// set of table names/aliases that are forced to be non-null if the expression
// evaluates to TRUE. This is the goopg analogue of PG's find_nonnullable_rels
// (clauses.c), simplified:
//
//   - Comparison operators (=, <>, <, <=, >, >=) are strict: ColumnRef operands'
//     table names are collected.
//   - IS NOT NULL constrains the tested column's table.
//   - Top-level AND: union of children.
//   - OR / NOT / function calls: too complex; return empty (conservative).
func collectNonNullableTableNames(e parser.Expr) map[string]bool {
	return collectNonNullableWalk(e, true)
}

func collectNonNullableWalk(e parser.Expr, topLevel bool) map[string]bool {
	if e == nil {
		return nil
	}
	result := make(map[string]bool)

	switch x := e.(type) {
	case *parser.BinaryOp:
		if isStrictCompareOp(x.Op) {
			for _, name := range collectColumnRefTableNames(x.Left) {
				result[name] = true
			}
			for _, name := range collectColumnRefTableNames(x.Right) {
				result[name] = true
			}
		}
		// Recurse into AND arms to collect nested strict quals.
		if x.Op == parser.OpAnd {
			left := collectNonNullableWalk(x.Left, topLevel)
			right := collectNonNullableWalk(x.Right, topLevel)
			for name := range left {
				result[name] = true
			}
			for name := range right {
				result[name] = true
			}
		}

	case *parser.IsNullExpr:
		// IS NOT NULL: the tested column must be non-null.
		if x.Negated {
			for _, name := range collectColumnRefTableNames(x.Operand) {
				result[name] = true
			}
		}
	}

	return result
}

// isStrictCompareOp reports whether a comparison operator is strict — that is,
// it returns NULL (not TRUE or FALSE) when any operand is NULL.
// =, <>, <, <=, >, >= are strict.
func isStrictCompareOp(op parser.OpCode) bool {
	switch op {
	case parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
		return true
	}
	return false
}

// collectColumnRefTableNames extracts table names from all ColumnRef nodes in
// an expression tree. Returns deduplicated names.
func collectColumnRefTableNames(e parser.Expr) []string {
	if e == nil {
		return nil
	}
	m := make(map[string]bool)
	collectColumnRefTableNamesWalk(e, m)
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	return names
}

func collectColumnRefTableNamesWalk(e parser.Expr, dst map[string]bool) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *parser.ColumnRef:
		if x.Table != "" {
			dst[x.Table] = true
		}
	case *parser.BinaryOp:
		collectColumnRefTableNamesWalk(x.Left, dst)
		collectColumnRefTableNamesWalk(x.Right, dst)
	case *parser.UnaryOp:
		collectColumnRefTableNamesWalk(x.Operand, dst)
	case *parser.IsNullExpr:
		collectColumnRefTableNamesWalk(x.Operand, dst)
	case *parser.FuncCall:
		for _, arg := range x.Args {
			collectColumnRefTableNamesWalk(arg, dst)
		}
	}
}

// rangeVarNames returns the names by which a RangeVar can be referenced:
// its alias (if any) and its relation name.
func rangeVarNames(rv parser.RangeVar) []string {
	if rv.Alias != "" {
		return []string{rv.Alias}
	}
	if rv.Name != "" {
		return []string{rv.Name}
	}
	return nil
}

// rangeVarPrimaryName returns the primary name for a RangeVar: alias if set,
// otherwise relation name.
func rangeVarPrimaryName(rv parser.RangeVar) string {
	if rv.Alias != "" {
		return rv.Alias
	}
	return rv.Name
}

// anyNameIn reports whether any name in names exists in set.
func anyNameIn(names []string, set map[string]bool) bool {
	for _, name := range names {
		if set[name] {
			return true
		}
	}
	return false
}
