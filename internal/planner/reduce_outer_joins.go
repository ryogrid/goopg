package planner

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

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
func reduceOuterJoins(from []parser.FromExpr, where parser.Expr, cat catalog.Catalog) {
	if where == nil {
		return
	}
	// Build a table-name → *catalog.Table lookup from the FROM clause so
	// collectNonNullableTableNames can resolve column types for operator
	// strictness checks.
	tableMap := buildTableMap(from, cat)
	nonnullable := collectNonNullableTableNames(where, tableMap, cat)
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

// buildTableMap builds a map from table name/alias to *catalog.Table by
// scanning the FROM clause's RangeVars and looking up each one in the catalog.
func buildTableMap(from []parser.FromExpr, cat catalog.Catalog) map[string]*catalog.Table {
	if cat == nil {
		return nil
	}
	m := make(map[string]*catalog.Table)
	for i := range from {
		collectTableNames(&from[i], cat, m)
	}
	return m
}

func collectTableNames(item *parser.FromExpr, cat catalog.Catalog, dst map[string]*catalog.Table) {
	// Base RangeVar
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: item.Base.Schema, Name: item.Base.Name})
	if ok {
		if item.Base.Alias != "" {
			dst[item.Base.Alias] = tbl
		} else {
			dst[item.Base.Name] = tbl
		}
	}
	// JoinExpr chain (each JoinExpr's Right is a RangeVar)
	for _, j := range item.Joins {
		tbl, ok := cat.LookupTable(parser.ObjectName{Schema: j.Right.Schema, Name: j.Right.Name})
		if ok {
			if j.Right.Alias != "" {
				dst[j.Right.Alias] = tbl
			} else {
				dst[j.Right.Name] = tbl
			}
		}
	}
}

// collectNonNullableTableNames walks a parser WHERE expression and returns the
// set of table names/aliases that are forced to be non-null if the expression
// evaluates to TRUE. This is the goopg analogue of PG's find_nonnullable_rels
// (clauses.c), simplified:
//
//   - Strict operators: ColumnRef operands' table names are collected.
//     Operator strictness is determined by:
//     a. Comparison operators (=, <>, <, <=, >, >=) are always strict (fast path).
//     b. For other operators, we resolve the operator OID via the catalog
//        operator index (LookupOperatorForNode) using the ColumnRef's column
//        type, then check proisstrict via IsStrictProc. When types can't be
//        resolved, we fall back conservatively (the operator is assumed not strict).
//   - IS NOT NULL constrains the tested column's table.
//   - Top-level AND: union of children.
//   - OR / NOT / function calls: too complex; return empty (conservative).
func collectNonNullableTableNames(e parser.Expr, tableMap map[string]*catalog.Table, cat catalog.Catalog) map[string]bool {
	return collectNonNullableWalk(e, true, tableMap, cat)
}

func collectNonNullableWalk(e parser.Expr, topLevel bool, tableMap map[string]*catalog.Table, cat catalog.Catalog) map[string]bool {
	if e == nil {
		return nil
	}
	result := make(map[string]bool)

	switch x := e.(type) {
	case *parser.BinaryOp:
		if isStrictOp(x.Op, x.Left, x.Right, tableMap, cat) {
			for _, name := range collectColumnRefTableNames(x.Left) {
				result[name] = true
			}
			for _, name := range collectColumnRefTableNames(x.Right) {
				result[name] = true
			}
		}
		// Recurse into AND arms to collect nested strict quals.
		if x.Op == parser.OpAnd {
			left := collectNonNullableWalk(x.Left, topLevel, tableMap, cat)
			right := collectNonNullableWalk(x.Right, topLevel, tableMap, cat)
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

// isStrictOp reports whether a binary operator is strict — that is, it returns
// NULL (not TRUE or FALSE) when any operand is NULL.
//
// Comparison operators (=, <>, <, <=, >, >=) are always strict in PG (every
// btree operator class member must be strict). For these we take the fast path
// without consulting the catalog.
//
// For other operators, we resolve column types from tableMap to look up the
// operator OID via LookupOperatorForNode, then check the underlying function's
// proisstrict flag via catalog.IsStrictProc. When operand types can't be
// resolved, we return false (conservative).
func isStrictOp(op parser.OpCode, left, right parser.Expr, tableMap map[string]*catalog.Table, cat catalog.Catalog) bool {
	// Fast path: comparison operators are always strict.
	if isStrictCompareOp(op) {
		return true
	}

	// For non-comparison operators, try to resolve operand types and check
	// strictness via the catalog.
	if tableMap == nil || cat == nil {
		return false
	}

	leftType := resolveExprType(left, tableMap, cat)
	rightType := resolveExprType(right, tableMap, cat)
	if leftType == 0 || rightType == 0 {
		return false // can't resolve types; conservative
	}

	opName := opToName(op)
	if opName == "" {
		return false
	}

	opEntry, ok := catalog.LookupOperatorForNode(opName, leftType, rightType)
	if !ok {
		return false
	}

	// Check proisstrict on the underlying function (oprcode).
	return catalog.IsStrictProc(opEntry.Code)
}

// resolveExprType tries to determine the OID type of a parser expression by
// looking up the column's type in tableMap. Returns 0 if the type can't be
// resolved.
func resolveExprType(e parser.Expr, tableMap map[string]*catalog.Table, cat catalog.Catalog) uint32 {
	colRef, ok := e.(*parser.ColumnRef)
	if !ok {
		return 0
	}
	if colRef.Table == "" {
		return 0
	}
	tbl, ok := tableMap[colRef.Table]
	if !ok {
		return 0
	}
	col, ok := cat.LookupColumn(tbl, colRef.Column)
	if !ok {
		return 0
	}
	return catalog.TypeNameToOID(col.Type.Name)
}

// opToName maps parser operator tokens to PG operator names (oprname spelling)
// for catalog lookup. Returns "" for operator tokens that don't correspond to
// a single PG operator (AND, OR, NOT, etc.).
func opToName(op parser.OpCode) string {
	switch op {
	case parser.OpEq:
		return "="
	case parser.OpNe:
		return "<>"
	case parser.OpLt:
		return "<"
	case parser.OpLe:
		return "<="
	case parser.OpGt:
		return ">"
	case parser.OpGe:
		return ">="
	case parser.OpLike:
		return "~~"
	case parser.OpILike:
		return "~~*"
	case parser.OpNotLike:
		return "!~~"
	case parser.OpNotILike:
		return "!~~*"
	case parser.OpRegexMatch:
		return "~"
	case parser.OpRegexIMatch:
		return "~*"
	case parser.OpRegexNoMatch:
		return "!~"
	case parser.OpRegexINoMatch:
		return "!~*"
	case parser.OpOverlap:
		return "&&"
	case parser.OpContains:
		return "@>"
	case parser.OpContainedBy:
		return "<@"
	case parser.OpConcat:
		return "||"
	default:
		return ""
	}
}

// isStrictCompareOp reports whether a comparison operator token is strict.
// All btree comparison operators (=, <>, <, <=, >, >=) are strict in PG —
// this is a fundamental requirement of the btree operator class, so the
// token check alone is sufficient without a catalog lookup.
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
