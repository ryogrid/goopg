package optimizer

import (
	"slices"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// reduceOuterJoins demotes outer joins to inner joins when a strict qual above
// them constrains the nullable side. It implements both the reduction (pass 2)
// and the RIGHT→LEFT flip of PG's reduce_outer_joins
// (postgres/src/backend/optimizer/prep/prepjointree.c).
//
// RIGHT→LEFT flip (S9.4): first-position RIGHT and FULL→RIGHT joins are
// flipped to LEFT by swapping Base↔Right. Deeper RIGHT/FULL→RIGHT joins
// need a nested JoinExpr AST (parser.FromExpr is a flat left-deep chain)
// and are ledgred — their RIGHT→ANTI path is unavailable.
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
	// These drive LEFT→ANTI demotion at S9.3. Unqualified refs are
	// resolved through tableMap+cat (unique column ownership), so a
	// CTE-shaped `WHERE wr_order_number IS NULL` attributes to web_returns
	// exactly as PG's analyzer would (varno assignment); ambiguous or
	// unresolvable refs are skipped (conservative = old behavior).
	upperFN := collectForcedNullTableNames(where, tableMap, cat)
	// R40/K69: column-granularity forced-null set for the S9.3 ANTI rule.
	upperFNCols := collectForcedNullColumnKeys(where, tableMap, cat)
	for i := range from {
		applyDemotion(&from[i], upperNN, upperFN, upperFNCols, tableMap, cat)
	}
}

// demotedForPlan returns a COPY of `item` carrying the same demotion verdicts
// `reduceOuterJoins` computes, WITHOUT the S9.4 RIGHT->LEFT flip (R27 §4a).
//
// The problem it solves: `reduceOuterJoins` runs AFTER `planFromClause` has
// built the node tree, so its demotions reach the jointree deconstruction but
// never the plan. The plan keeps `JoinTypeLeft` while `root->join_info_list`
// (built from the demoted items) says there is no outer join;
// `outerLinksHaveSJInfos` then declines the statement, and a declined
// statement cannot converge on PG's plan by any costing work (K27). Measured:
// TPC-DS Q49 emits 3 `Hash Left Join` where PG emits none at all.
//
// Why not simply move the call, and why not suppress the flip — both were
// tried and measured:
//
//   - MOVING IT breaks values. The S9.4 flip SWAPS `Base` and
//     `Joins[0].Right`, and `planFromItem` assigns
//     `SchemaColumn.SourceTableIdx` and binding offsets in FROM ORDER, so a
//     swapped item re-points column references. PG is immune because it
//     references columns by `Var`.
//   - SUPPRESSING THE FLIP breaks five tests that pin it: the jointree
//     deconstruction consumes it, so it is contractual, not an internal
//     detail of the analysis.
//
// So the analysis runs on a throwaway copy and only the join-TYPE verdicts are
// transplanted onto a fresh copy in the ORIGINAL orientation. The caller plans
// from that; `s.FromExprs` is untouched, so the late `reduceOuterJoins` call
// and everything downstream behave exactly as before. The two consumers then
// agree on join TYPE — all `outerLinksHaveSJInfos` compares — while each keeps
// the representation it needs.
//
// Only the INNER verdict is transplanted. It is orientation-free, so a
// verdict reached in the flipped orientation still applies to the original;
// and it drops no qual. LEFT->ANTI is declined because that conversion is not
// plan-complete in goopg (see the loop). An un-demoted join is today's
// behaviour, a mis-converted one is wrong rows.
// demotedForPlan's second return value is the set of "table\x00column" keys
// whose `IS NULL` conjunct forced a LEFT->ANTI conversion it transplanted in
// this item — R40/K69. Empty (nil) in the common case where no ANTI verdict
// fired. The caller unions this across every s.FromExprs item and uses it to
// strip exactly those conjuncts from WHERE before it is resolved
// (stripForcingNullQuals); see planner.go's antiForcedNullCols.
//
// COLUMN keys, not table names: PG's ANTI rule is var-granular
// (mbms_overlap_sets), so a table may have one column forcing the conversion
// and others doing unrelated filtering work that must survive.
func demotedForPlan(item parser.FromExpr, where parser.Expr, cat catalog.Catalog) (parser.FromExpr, map[string]bool) {
	if len(item.Joins) == 0 {
		return item, nil
	}
	tableMap := buildTableMap([]parser.FromExpr{item}, cat)
	upperNN := collectNonNullableTableNames(where, tableMap, cat)
	upperFN := collectForcedNullTableNames(where, tableMap, cat)
	upperFNCols := collectForcedNullColumnKeys(where, tableMap, cat)

	work := item
	work.Joins = make([]parser.JoinExpr, len(item.Joins))
	copy(work.Joins, item.Joins)
	antiForcedCols := applyDemotion(&work, upperNN, upperFN, upperFNCols, tableMap, cat)

	out := item
	out.Joins = make([]parser.JoinExpr, len(item.Joins))
	copy(out.Joins, item.Joins)
	for i := range out.Joins {
		got := work.Joins[i].Type
		if got == out.Joins[i].Type {
			continue
		}
		// R40/K69: the LEFT->ANTI verdict (S9.3) is now transplanted too.
		// It was declined here until now because PG's conversion also DROPS
		// the `IS NULL` qual that forced it — an anti-join's output has no
		// nullable-side columns for it to test — and goopg's plan-tree
		// transplant used to leave both the type AND the qual untouched,
		// which filters every surviving row (0 rows where 1 is correct,
		// measured on `TestLeftJoinSearchAdmissionValues`). The caller now
		// completes the conversion: it strips the specific forcing qual
		// (stripForcingNullQuals) using the table name recorded here, and
		// `mapJoinType` (planner.go) maps this verdict to `JoinTypeAnti`
		// with the per-item schema narrowed to match (planFromItem, R40 §4d)
		// — an un-narrowed schema would let a later join in the same chain
		// (Q78's `... JOIN date_dim`) resolve columns at offsets that still
		// budget space for the now-invisible nullable side.
		if got == parser.JoinAnti {
			out.Joins[i].Type = parser.JoinAnti
			continue
		}
		// ONLY INNER and ANTI verdicts are transplanted, deliberately.
		// INNER is safe unconditionally: it is orientation-free (so the
		// flipped first join needs no special case) and it drops no qual —
		// a strict qual that licensed the demotion stays true above the
		// join. Any other verdict declines: an un-demoted join is today's
		// shipped behaviour, a mis-converted one is wrong rows.
		if got != parser.JoinInner {
			continue
		}
		out.Joins[i].Type = parser.JoinInner
	}
	return out, antiForcedCols
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
// S9.4 RIGHT→LEFT flip (PG reduce_outer_joins_pass2 lines 3366-3376):
// RIGHT joins are flipped to LEFT before demotion checks so that LEFT→ANTI
// and other LEFT-specific reductions apply. The flip swaps the join's children
// (larg↔rarg in PG). In goopg's flat chain, this means swapping Base↔Right
// for the first join. Deeper RIGHT joins need a nested AST and are ledgred.
//
// FULL→RIGHT→LEFT: when only the right side of a FULL join is constrained,
// PG demotes FULL→RIGHT first (prepjointree.c:3334-3340), then the
// RIGHT→LEFT flip at line 3366 converts it. We do both in one step for the
// simple (first-join) case: swap Base↔Right and change to LEFT.
//
// Parameters:
//   - upperNN: nonnullable rels from the WHERE clause (the "upper" set).
//     This is the starting accumulated set; it is never mutated.
//
// applyDemotion returns the set of "table\x00column" keys whose IS NULL
// conjunct forced a LEFT->ANTI conversion (R40/K69). Empty unless an ANTI
// verdict fired; `demotedForPlan` hands these to `stripForcingNullQuals`,
// since PG's conversion also drops exactly those conjuncts.
func applyDemotion(item *parser.FromExpr, upperNN, upperFN, upperFNCols map[string]bool, tableMap map[string]*catalog.Table, cat catalog.Catalog) map[string]bool {
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
	// accumulatedFNCols is the COLUMN-granularity twin of accumulatedFN, and
	// is what the S9.3 LEFT->ANTI rule actually tests (R40/K69): PG's ANTI
	// check is find_nonnullable_VARS vs forced_null_vars, not the relation-
	// level set the INNER check uses. It propagates by the identical rules.
	accumulatedFNCols := make(map[string]bool, len(upperFNCols))
	for key := range upperFNCols {
		accumulatedFNCols[key] = true
	}
	// antiForced accumulates the forcing column keys of every ANTI verdict
	// this walk reaches.
	var antiForced map[string]bool

	// S9.4: RIGHT→LEFT flip for the first join (PG pass2 lines 3366-3376).
	// This normalises RIGHT to LEFT so that LEFT-specific reductions
	// (LEFT→ANTI, etc.) apply. For the first join, the swap is trivial:
	// RIGHT(A,B) → LEFT(B,A) by swapping Base↔Right.
	if len(item.Joins) > 0 {
		first := &item.Joins[0]
		if first.Type == parser.JoinRight {
			item.Base, first.Right = first.Right, item.Base
			first.Type = parser.JoinLeft
		} else if first.Type == parser.JoinFull {
			// FULL→RIGHT→LEFT: when only the right side is constrained,
			// PG demotes FULL→RIGHT, then RIGHT→LEFT flip. Do both at once.
			// PG prepjointree.c:3334-3340 (FULL→RIGHT) + 3366-3376 (flip).
			baseName := rangeVarPrimaryName(item.Base)
			rightName := rangeVarPrimaryName(first.Right)
			if !accumulatedNN[baseName] && accumulatedNN[rightName] {
				item.Base, first.Right = first.Right, item.Base
				first.Type = parser.JoinLeft
			}
		}
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
		localFN := collectForcedNullTableNames(j.On, tableMap, cat)
		// Column-granularity twins for the S9.3 ANTI rule (R40/K69).
		localNNCols := collectNonNullableColumnKeys(j.On, tableMap, cat)
		localFNCols := collectForcedNullColumnKeys(j.On, tableMap, cat)

		// ---- demotion check ----
		switch j.Type {
		case parser.JoinLeft:
			// Left side is preserved, right side is nullable.
			if accumulatedNN[rightName] {
				j.Type = parser.JoinInner
			}
			// S9.3 LEFT→ANTI: the SAME right-side COLUMN is forced-null
			// from upper quals (IS NULL) and held non-null by this join's
			// own ON clause → the ON can never be TRUE for any row where
			// the upper quals pass → ANTI join suffices.
			// PG reduce_outer_joins_pass2 lines 3379-3403.
			//
			// R40/K69: this test is COLUMN-granular, matching PG's
			// `mbms_overlap_sets(nonnullable_vars, forced_null_vars)`.
			// It used to compare TABLE names, which over-fires: in
			// `a LEFT JOIN b ON a.id = b.id WHERE b.y IS NULL` the ON is
			// strict for b.id while the WHERE forces b.y — different
			// columns, and PG keeps the LEFT join (oracle-verified:
			// `Merge Left Join` + `Filter: (b.y IS NULL)`). The relation-
			// level set is still correct for the LEFT->INNER check above,
			// which is PG's find_nonnullable_RELS — the two granularities
			// are deliberate on both sides.
			if forcing := antiForcingColumns(accumulatedFNCols, localNNCols, rangeVarNames(j.Right)); j.Type == parser.JoinLeft && len(forcing) > 0 {
				j.Type = parser.JoinAnti
				if antiForced == nil {
					antiForced = make(map[string]bool)
				}
				for key := range forcing {
					antiForced[key] = true
				}
			}

		case parser.JoinRight:
			// RIGHT join not at first position (first-position RIGHT was
			// flipped to LEFT above). Only RIGHT→INNER demotion is possible
			// without the flip; RIGHT→ANTI needs a nested AST.
			// S9.4 ledger: deeper RIGHT joins can't be flipped.
			//
			// R27/2 (K29): judged against `upperNN` — quals from ABOVE — and
			// NOT `accumulatedNN`, which also carries ON-clause strictness
			// merged from INNER joins BELOW this one. Those do not survive
			// this join: its nullable side IS that accumulated left arm, and
			// it null-extends the arm after the inner joins produced it.
			//
			// Using the accumulated set demoted RIGHT→INNER on
			//   rj_a JOIN rj_b ON rj_a.id = rj_b.aid
			//   RIGHT JOIN rj_c ON rj_b.cid = rj_c.id  WHERE rj_a.id IS NULL
			// because the inner ON is strict on {rj_a, rj_b} — destroying the
			// null-extended rows that are the only rows the query returns
			// (0 rows where 1 is correct). PG does not make this mistake:
			// `reduce_outer_joins_pass2` only lets quals from above a join
			// constrain it.
			//
			// Conservative by construction: `upperNN` is a subset of
			// `accumulatedNN`, so this can only DECLINE demotions that were
			// previously made, never add one. An un-demoted join is today's
			// shipped behaviour; a wrongly-demoted one drops rows.
			if anyNameIn(leftNames, upperNN) {
				j.Type = parser.JoinInner
			}

		case parser.JoinFull:
			// Both sides are nullable. Check each independently.
			// PG prepjointree.c:3319-3341.
			// R27/2 (K29): same rule as the RIGHT arm above — a FULL join's
			// LEFT side is nullable too, so strictness merged from INNER
			// joins below does not survive it. Quals from above only.
			leftConstrained := anyNameIn(leftNames, upperNN)
			rightConstrained := accumulatedNN[rightName]
			if leftConstrained && rightConstrained {
				j.Type = parser.JoinInner
			} else if leftConstrained {
				// Only left constrained → LEFT (PG: JOIN_LEFT).
				// Left side is nonnullable, right side is still nullable.
				j.Type = parser.JoinLeft
			} else if rightConstrained {
				// Only right constrained.
				// First join: already flipped by pre-loop above → won't reach here.
				// Deeper joins: FULL→RIGHT needed, but RIGHT is unrepresentable
				// in the flat chain. Ledgered at S9.4.
			}
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
			for key := range localFNCols {
				accumulatedFNCols[key] = true
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
			// survive.
			// NOTE: first-position RIGHT is flipped to LEFT above, so this
			// branch is now dead for i==0. It remains for deeper chains where
			// the flip is unrepresentable (S9.4 ledger).
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
			nextFNCols := make(map[string]bool)
			for key := range localFNCols {
				if containsName(rangeVarNames(j.Right), colKeyTable(key)) {
					nextFNCols[key] = true
				}
			}
			accumulatedFNCols = nextFNCols

		case parser.JoinFull:
			// Both sides nullable — nothing propagates through.
			accumulatedNN = make(map[string]bool)
			accumulatedFN = make(map[string]bool)
			accumulatedFNCols = make(map[string]bool)
		}

		// Accumulate right side's names for the next iteration.
		leftNames = append(leftNames, rangeVarNames(j.Right)...)
	}
	return antiForced
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
			for _, name := range collectColumnRefTableNames(x.Left, tableMap, cat) {
				result[name] = true
			}
			for _, name := range collectColumnRefTableNames(x.Right, tableMap, cat) {
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
			for _, name := range collectColumnRefTableNames(x.Operand, tableMap, cat) {
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
	tbl, ok := tableMap[colRef.Table]
	if !ok && colRef.Table == "" {
		// Unqualified ref: resolve through unique column ownership
		// (same rule as collectColumnRefTableNamesWalk).
		var owner string
		if owner, ok = resolveUnqualifiedOwner(colRef.Column, tableMap, cat); ok {
			tbl, ok = tableMap[owner]
		}
	}
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
//
// Qualified refs record their qualifier. Unqualified refs are resolved through
// unique column ownership over tableMap (a name/alias → *catalog.Table map as
// built by buildTableMap): the ref attributes to the scope table iff exactly
// one scope table owns the column. This mirrors what PG's analyzer does when
// it assigns varnos (an unqualified column owned by exactly one RTE resolves
// there; owned by zero or two+ is an error) — except errors become silent
// skips, which is the conservative direction (fewer demotions, never a wrong
// answer). tableMap or cat nil → unqualified refs are skipped (old behavior).
func collectColumnRefTableNames(e parser.Expr, tableMap map[string]*catalog.Table, cat catalog.Catalog) []string {
	if e == nil {
		return nil
	}
	m := make(map[string]bool)
	collectColumnRefTableNamesWalk(e, m, tableMap, cat)
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	return names
}

// resolveUnqualifiedOwner reports the tableMap key of the single scope table
// owning column, or false when cat/tableMap is unavailable or ownership is
// not unique (zero or ambiguous). Uniqueness is order-independent, so map
// iteration order cannot affect the result.
func resolveUnqualifiedOwner(column string, tableMap map[string]*catalog.Table, cat catalog.Catalog) (string, bool) {
	if cat == nil || len(tableMap) == 0 || column == "" {
		return "", false
	}
	owner := ""
	count := 0
	for key, tbl := range tableMap {
		if tbl == nil {
			continue
		}
		if _, ok := cat.LookupColumn(tbl, column); ok {
			owner = key
			count++
			if count > 1 {
				return "", false
			}
		}
	}
	if count != 1 {
		return "", false
	}
	return owner, true
}

func collectColumnRefTableNamesWalk(e parser.Expr, dst map[string]bool, tableMap map[string]*catalog.Table, cat catalog.Catalog) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *parser.ColumnRef:
		if x.Table != "" {
			dst[x.Table] = true
		} else if owner, ok := resolveUnqualifiedOwner(x.Column, tableMap, cat); ok {
			dst[owner] = true
		}
	case *parser.BinaryOp:
		collectColumnRefTableNamesWalk(x.Left, dst, tableMap, cat)
		collectColumnRefTableNamesWalk(x.Right, dst, tableMap, cat)
	case *parser.UnaryOp:
		collectColumnRefTableNamesWalk(x.Operand, dst, tableMap, cat)
	case *parser.IsNullExpr:
		collectColumnRefTableNamesWalk(x.Operand, dst, tableMap, cat)
	case *parser.FuncCall:
		for _, arg := range x.Args {
			collectColumnRefTableNamesWalk(arg, dst, tableMap, cat)
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
func collectForcedNullTableNames(e parser.Expr, tableMap map[string]*catalog.Table, cat catalog.Catalog) map[string]bool {
	return collectForcedNullWalk(e, true, tableMap, cat)
}

func collectForcedNullWalk(e parser.Expr, topLevel bool, tableMap map[string]*catalog.Table, cat catalog.Catalog) map[string]bool {
	if e == nil {
		return nil
	}
	result := make(map[string]bool)

	switch x := e.(type) {
	case *parser.IsNullExpr:
		// IS NULL (not IS NOT NULL): the column IS forced to be null if the
		// clause passes.
		if !x.Negated {
			for _, name := range collectColumnRefTableNames(x.Operand, tableMap, cat) {
				result[name] = true
			}
		}

	case *parser.BinaryOp:
		// AND: union of children (at top level only — PG's
		// find_forced_null_vars does not descend into OR).
		if x.Op == parser.OpAnd && topLevel {
			left := collectForcedNullWalk(x.Left, true, tableMap, cat)
			right := collectForcedNullWalk(x.Right, true, tableMap, cat)
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

// stripForcingNullQuals returns a REWRITTEN copy of `where` with the
// top-level `IS NULL` conjunct(s) that forced a LEFT->ANTI demotion
// (R40/K69, `applyDemotion`'s S9.3 rule) elided. It mirrors
// `collectForcedNullWalk`'s top-level-AND-only descent exactly, so it can
// only drop a conjunct that function already certified as forcing the
// demotion — never a nested OR/NOT/function-call qual PG's own
// `find_forced_null_vars` would not have looked at either.
//
// This is PG's own rule, not an invented one: `reduce_outer_joins`'s
// comment (prepjointree.c) states "The IS NULL clause then becomes
// redundant, and must be removed to prevent bogus selectivity
// calculations" — an ANTI join's output has no nullable-side column left
// for that qual to test, so leaving it in place would filter every
// surviving row instead of doing nothing (K30).
//
// `where` itself is never mutated — same discipline `canonicalizeQual`
// already uses one call site below this in `planSelect`; the parse tree
// is shared with view/rule deparsers. Returns nil when every top-level
// conjunct was elided (a WHERE clause that was ONLY the forcing IS NULL
// test); callers must treat a nil result as "no Filter node needed", the
// same convention the single-relation arm already uses for
// restriction_is_always_true.
func stripForcingNullQuals(where parser.Expr, antiCols map[string]bool, tableMap map[string]*catalog.Table, cat catalog.Catalog) parser.Expr {
	if len(antiCols) == 0 {
		return where
	}
	return stripForcingNullWalk(where, true, antiCols, tableMap, cat)
}

func stripForcingNullWalk(e parser.Expr, topLevel bool, antiCols map[string]bool, tableMap map[string]*catalog.Table, cat catalog.Catalog) parser.Expr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *parser.IsNullExpr:
		// Mirror collectForcedNullColumnKeys' certification exactly: only a
		// non-negated IS NULL whose operand's referenced COLUMNS are ALL in
		// antiCols is dropped — the same keys `antiForcingColumns` certified
		// as forcing the conversion. A multi-column operand mixing a forcing
		// column with a non-forcing one is conservatively kept, since the
		// latter may still be doing real filtering work.
		if !x.Negated {
			keys := collectColumnRefColumnKeys(x.Operand, tableMap, cat)
			if len(keys) > 0 {
				allForced := true
				for _, key := range keys {
					if !antiCols[key] {
						allForced = false
						break
					}
				}
				if allForced {
					return nil
				}
			}
		}
		return e

	case *parser.BinaryOp:
		// AND: strip each child independently (top level only — same
		// no-OR-descent rule as collectForcedNullWalk). A dropped child
		// collapses the AND to its surviving sibling; dropping both
		// collapses to nil (no residual predicate at this node).
		if x.Op == parser.OpAnd && topLevel {
			left := stripForcingNullWalk(x.Left, true, antiCols, tableMap, cat)
			right := stripForcingNullWalk(x.Right, true, antiCols, tableMap, cat)
			switch {
			case left == nil && right == nil:
				return nil
			case left == nil:
				return right
			case right == nil:
				return left
			case left == x.Left && right == x.Right:
				return e
			default:
				out := *x
				out.Left = left
				out.Right = right
				return &out
			}
		}
	}
	return e
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

// ---- column-granularity variants for the S9.3 LEFT->ANTI rule (R40/K69) ----
//
// PG uses TWO different granularities in reduce_outer_joins_pass2, and the
// difference is load-bearing:
//
//   - the LEFT->INNER reduction tests `bms_overlap(nonnullable_rels,
//     right_state->relids)` — find_nonnullable_RELS, RELATION granularity;
//   - the LEFT->ANTI reduction tests `mbms_overlap_sets(nonnullable_vars,
//     forced_null_vars)` — find_nonnullable_VARS, COLUMN granularity
//     (prepjointree.c:3379-3403).
//
// goopg used table granularity for BOTH, which is correct for INNER and
// OVER-EAGER for ANTI: `a LEFT JOIN b ON a.id = b.id WHERE b.y IS NULL` has
// the ON strict for `b.id` and the WHERE forcing `b.y` — DIFFERENT columns, so
// PG keeps the LEFT join and applies the IS NULL as a filter (verified on the
// 18.3 oracle: `Merge Left Join` + `Filter: (b.y IS NULL)`). goopg demoted it
// to ANTI. That was invisible while no ANTI verdict reached a plan (K30); the
// R40 transplant made it reachable, and it showed up as
// `TestNonSpineOuterAdmissionValues` failing to resolve the nullable-side
// column an ANTI join no longer publishes.
//
// Q78's real shape is unaffected and still converts, because there the SAME
// column carries both roles: `ON wr_order_number = ws_order_number ...
// WHERE wr_order_number IS NULL` (oracle: `Merge Anti Join`, IS NULL dropped).
//
// Keys are "table\x00column" — table is the alias when one is present, matching
// rangeVarPrimaryName / collectColumnRefTableNames' own attribution rule.

const colKeySep = "\x00"

func makeColKey(table, column string) string { return table + colKeySep + column }

// colKeyTable returns the table part of a key produced by makeColKey.
func colKeyTable(key string) string {
	if i := strings.Index(key, colKeySep); i >= 0 {
		return key[:i]
	}
	return key
}

// collectColumnRefColumnKeys is collectColumnRefTableNames at column
// granularity: it attributes each ColumnRef by the same rule (qualifier when
// present, unique column ownership otherwise) but keeps the column name too.
func collectColumnRefColumnKeys(e parser.Expr, tableMap map[string]*catalog.Table, cat catalog.Catalog) []string {
	if e == nil {
		return nil
	}
	m := make(map[string]bool)
	collectColumnRefColumnKeysWalk(e, m, tableMap, cat)
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func collectColumnRefColumnKeysWalk(e parser.Expr, dst map[string]bool, tableMap map[string]*catalog.Table, cat catalog.Catalog) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *parser.ColumnRef:
		if x.Table != "" {
			dst[makeColKey(x.Table, x.Column)] = true
		} else if owner, ok := resolveUnqualifiedOwner(x.Column, tableMap, cat); ok {
			dst[makeColKey(owner, x.Column)] = true
		}
	case *parser.BinaryOp:
		collectColumnRefColumnKeysWalk(x.Left, dst, tableMap, cat)
		collectColumnRefColumnKeysWalk(x.Right, dst, tableMap, cat)
	case *parser.UnaryOp:
		collectColumnRefColumnKeysWalk(x.Operand, dst, tableMap, cat)
	case *parser.IsNullExpr:
		collectColumnRefColumnKeysWalk(x.Operand, dst, tableMap, cat)
	case *parser.FuncCall:
		for _, arg := range x.Args {
			collectColumnRefColumnKeysWalk(arg, dst, tableMap, cat)
		}
	}
}

// collectNonNullableColumnKeys is collectNonNullableTableNames at column
// granularity — goopg's find_nonnullable_vars. Same descent and same
// strictness test; only the key is finer.
func collectNonNullableColumnKeys(e parser.Expr, tableMap map[string]*catalog.Table, cat catalog.Catalog) map[string]bool {
	result := make(map[string]bool)
	collectNonNullableColumnKeysWalk(e, result, tableMap, cat)
	return result
}

func collectNonNullableColumnKeysWalk(e parser.Expr, dst map[string]bool, tableMap map[string]*catalog.Table, cat catalog.Catalog) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *parser.BinaryOp:
		if isStrictOp(x.Op, x.Left, x.Right, tableMap, cat) {
			for _, key := range collectColumnRefColumnKeys(x.Left, tableMap, cat) {
				dst[key] = true
			}
			for _, key := range collectColumnRefColumnKeys(x.Right, tableMap, cat) {
				dst[key] = true
			}
		}
		if x.Op == parser.OpAnd {
			collectNonNullableColumnKeysWalk(x.Left, dst, tableMap, cat)
			collectNonNullableColumnKeysWalk(x.Right, dst, tableMap, cat)
		}

	case *parser.IsNullExpr:
		// IS NOT NULL: the tested column must be non-null.
		if x.Negated {
			for _, key := range collectColumnRefColumnKeys(x.Operand, tableMap, cat) {
				dst[key] = true
			}
		}
	}
}

// collectForcedNullColumnKeys is collectForcedNullTableNames at column
// granularity — goopg's find_forced_null_vars. Same top-level-AND-only
// descent (never into OR), matching PG.
func collectForcedNullColumnKeys(e parser.Expr, tableMap map[string]*catalog.Table, cat catalog.Catalog) map[string]bool {
	result := make(map[string]bool)
	collectForcedNullColumnKeysWalk(e, true, result, tableMap, cat)
	return result
}

func collectForcedNullColumnKeysWalk(e parser.Expr, topLevel bool, dst map[string]bool, tableMap map[string]*catalog.Table, cat catalog.Catalog) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *parser.IsNullExpr:
		if !x.Negated {
			for _, key := range collectColumnRefColumnKeys(x.Operand, tableMap, cat) {
				dst[key] = true
			}
		}
	case *parser.BinaryOp:
		if x.Op == parser.OpAnd && topLevel {
			collectForcedNullColumnKeysWalk(x.Left, true, dst, tableMap, cat)
			collectForcedNullColumnKeysWalk(x.Right, true, dst, tableMap, cat)
		}
	}
}

// antiForcingColumns returns the columns of `rightNames` that carry BOTH roles
// PG's ANTI test requires — forced null from above AND non-nullable by this
// join's own quals — i.e. `mbms_overlap_sets(nonnullable_vars,
// forced_null_vars)` restricted to the join's right relids. A non-empty result
// is exactly PG's `bms_overlap(overlap, right_state->relids)` being true, and
// the keys ARE the conjuncts to drop when the conversion is transplanted to a
// plan (stripForcingNullQuals).
func antiForcingColumns(accumulatedFNCols, localNNCols map[string]bool, rightNames []string) map[string]bool {
	if len(accumulatedFNCols) == 0 || len(localNNCols) == 0 {
		return nil
	}
	var out map[string]bool
	for key := range accumulatedFNCols {
		if !localNNCols[key] {
			continue
		}
		if !containsName(rightNames, colKeyTable(key)) {
			continue
		}
		if out == nil {
			out = make(map[string]bool)
		}
		out[key] = true
	}
	return out
}
