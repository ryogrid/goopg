package planner

import (
	"slices"

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
//     WHERE passes. This is the "upper" nonnullable set.
//  2. For each FromExpr, walk its flat join chain. At each join:
//     a. Compute local nonnullable rels from the ON clause.
//     b. Check whether the nullable side overlaps the accumulated nonnullable
//        set (upper + any merged from prior INNER JOIN ON clauses). If so,
//        demote.
//     c. Propagate: INNER merges local ON findings into the accumulated set
//        for subsequent joins; LEFT passes only the upper set forward (the
//        right side can be null-extended); RIGHT/FULL reset or narrow the set.
//
// It modifies from in-place: JoinExpr.Type is changed when demotion fires.
// This is a pessimisation fix — it never produces a wrong answer, only
// opportunities it misses for lack of strictness information.
func reduceOuterJoins(from []parser.FromExpr, where parser.Expr, cat catalog.Catalog) {
	// Build a table-name → *catalog.Table lookup from the FROM clause so
	// collectNonNullableTableNames can resolve column types for operator
	// strictness checks.
	tableMap := buildTableMap(from, cat)
	upperNN := collectNonNullableTableNames(where, tableMap, cat)
	// Collect forced-null table names from WHERE (IS NULL predicates).
	// These drive LEFT→ANTI demotion at S9.3.
	upperFN := collectForcedNullTableNames(where)
	for i := range from {
		applyDemotion(&from[i], upperNN, upperFN, tableMap, cat)
	}
}

// applyDemotion walks one FromExpr's flat join chain and demotes outer joins
// whose nullable side is constrained by strict quals. The nonnullable set
// evolves as we walk the chain: local ON-clause findings are merged for INNER
// joins (PG's reduce_outer_joins_pass2 propagation).
//
// The join chain is strictly left-deep: (((Base ⋈ J0.Right) ⋈ J1.Right) ⋈ …).
// For a LEFT JOIN at position i, the right (nullable) side is Joins[i].Right
// (a single RangeVar). For a RIGHT JOIN, the left (accumulated) side is
// nullable and we check whether ANY of its tables are in nonnullable. For a
// FULL JOIN, each side is checked independently.
//
// Parameters:
//   - upperNN: nonnullable rels from the WHERE clause (the "upper" set).
//     This is the starting accumulated set; it is never mutated.
func applyDemotion(item *parser.FromExpr, upperNN, upperFN map[string]bool, tableMap map[string]*catalog.Table, cat catalog.Catalog) {
	// accumulatedNN is the working set that evolves as we walk the chain.
	// It starts as a copy of upperNN (WHERE-clause findings).
	accumulatedNN := make(map[string]bool, len(upperNN))
	for name := range upperNN {
		accumulatedNN[name] = true
	}
	// accumulatedFN is the parallel forced-null set (IS NULL predicates).
	// It drives LEFT→ANTI demotion (S9.3) and propagates by the same rules
	// as accumulatedNN.
	accumulatedFN := make(map[string]bool, len(upperFN))
	for name := range upperFN {
		accumulatedFN[name] = true
	}

	// Accumulated set of table names on the left.
	leftNames := rangeVarNames(item.Base)

	for i := range item.Joins {
		j := &item.Joins[i]
		rightName := rangeVarPrimaryName(j.Right)

		// Compute local nonnullable and forced-null rels from the ON clause.
		// For INNER joins these become real constraints on the result and
		// propagate to subsequent joins; for outer joins they only apply
		// within the nullable side (which may still be null-extended).
		localNN := collectNonNullableTableNames(j.On, tableMap, cat)
		localFN := collectForcedNullTableNames(j.On)

		// ---- demotion check ----
		switch j.Type {
		case parser.JoinLeft:
			// Left side is preserved, right side is nullable.
			if accumulatedNN[rightName] {
				j.Type = parser.JoinInner
			}
			// S9.3 LEFT→ANTI: right-side table is forced-null from upper
			// quals (IS NULL) AND appears in a strict position in the ON
			// clause → the ON can never be TRUE for any row where the
			// upper quals pass → ANTI join suffices.
			// PG reduce_outer_joins_pass2 lines 3388-3403.
			if j.Type == parser.JoinLeft && accumulatedFN[rightName] && localNN[rightName] {
				j.Type = parser.JoinAnti
			}

		case parser.JoinRight:
			// Right side is preserved, left (accumulated) side is nullable.
			if anyNameIn(leftNames, accumulatedNN) {
				j.Type = parser.JoinInner
			}

		case parser.JoinFull:
			// Both sides are nullable. Check each independently.
			leftConstrained := anyNameIn(leftNames, accumulatedNN)
			rightConstrained := accumulatedNN[rightName]
			if leftConstrained && rightConstrained {
				j.Type = parser.JoinInner
			} else if rightConstrained {
				// Right constrained → left preserved → becomes LEFT.
				j.Type = parser.JoinLeft
			}
			// Left-only constrained: would become RIGHT, but the flip
			// is unrepresentable — leave as FULL. (Ledger.)
		}

		// ---- propagation: update accumulatedNN + accumulatedFN for next iteration ----
		// PG reduce_outer_joins_pass2: inner merges upper+local; outer
		// passes local only to the nullable side, upper only to the
		// preserved side. In a left-deep chain the "preserved left"
		// continues to the next join, so the rules are:
		switch j.Type {
		case parser.JoinInner:
			// Both sides are truly combined — ON clause strict quals are
			// real constraints on the result. Merge local findings.
			for name := range localNN {
				accumulatedNN[name] = true
			}
			for name := range localFN {
				accumulatedFN[name] = true
			}

		case parser.JoinLeft, parser.JoinAnti:
			// Preserved left side keeps the upper set unchanged.
			// The right (nullable) side can be null-extended (LEFT) or
			// excluded (ANTI), so its tables do NOT join the accumulated
			// nonnullable set. accumulatedNN/accumulatedFN stay as-is.

		case parser.JoinRight:
			// Preserved right, nullable left. The left (accumulated)
			// side may be null-extended for subsequent joins, so reset
			// accumulatedNN: only right-side tables constrained by localNN
			// survive. (S9.4 will make this dead code after the flip.)
			next := make(map[string]bool)
			for name := range localNN {
				if name == rightName || containsName(rangeVarNames(j.Right), name) {
					next[name] = true
				}
			}
			accumulatedNN = next
			// Same reset for forced-null.
			nextFN := make(map[string]bool)
			for name := range localFN {
				if name == rightName || containsName(rangeVarNames(j.Right), name) {
					nextFN[name] = true
				}
			}
			accumulatedFN = nextFN

		case parser.JoinFull:
			// Both sides nullable — nothing propagates through.
			accumulatedNN = make(map[string]bool)
			accumulatedFN = make(map[string]bool)
		}

		// Accumulate right side's names for the next iteration.
		leftNames = append(leftNames, rangeVarNames(j.Right)...)
	}
}

// containsName reports whether name is present in names.
func containsName(names []string, name string) bool {
	return slices.Contains(names, name)
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

// collectForcedNullTableNames walks a WHERE expression and returns the set of
// table names/aliases whose columns are tested with IS NULL at the top level.
// This is the goopg analogue of PG's find_forced_null_vars (clauses.c), simplified
// to table-level granularity.
//
// Only top-level IS NULL and AND are examined: PG's find_forced_null_vars only
// checks NullTest IS_NULL at the top level and AND combinations, but does not
// descend into OR, NOT, or function calls.
func collectForcedNullTableNames(e parser.Expr) map[string]bool {
	return collectForcedNullWalk(e, true)
}

func collectForcedNullWalk(e parser.Expr, topLevel bool) map[string]bool {
	if e == nil {
		return nil
	}
	result := make(map[string]bool)

	switch x := e.(type) {
	case *parser.IsNullExpr:
		// IS NULL (not IS NOT NULL): the column IS forced to be null if the
		// clause passes.
		if !x.Negated {
			for _, name := range collectColumnRefTableNames(x.Operand) {
				result[name] = true
			}
		}

	case *parser.BinaryOp:
		// AND: union of children (at top level only — PG's
		// find_forced_null_vars does not descend into OR).
		if x.Op == parser.OpAnd && topLevel {
			left := collectForcedNullWalk(x.Left, true)
			right := collectForcedNullWalk(x.Right, true)
			for name := range left {
				result[name] = true
			}
			for name := range right {
				result[name] = true
			}
		}
	}

	return result
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
