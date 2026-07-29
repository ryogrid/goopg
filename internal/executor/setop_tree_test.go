package executor

// setop_tree_test.go — M0125-0020, accepted BY VALUE.
//
// A set-op chain used to be a linked list, so a parenthesised head branch and
// the whole compound were ONE SelectStmt sharing ONE ORDER BY / LIMIT / OFFSET
// slot. Two PostgreSQL behaviours were therefore unreachable, no matter how the
// slot was arbitrated:
//
//	(A ORDER BY 1 LIMIT 2) UNION ALL (C) ORDER BY 1 DESC
//	   — the inner sort/limit and the outer sort both need the slot;
//	((A ORDER BY 1 LIMIT 2) UNION ALL C) LIMIT 1
//	   — the inner LIMIT was overwritten by the outer one.
//
// gram.y makes `select_with_parens` a LEAF operand of `select_clause`, so text
// after the ')' always attaches to a node ABOVE the parenthesised query and
// transformSetOperationStmt (postgres/src/backend/parser/analyze.c) receives a
// tree. goopg now builds that node — a "grouping node", SelectStmt.SetOpOperand
// — which retires ParenBranches, InnerSegmentCount and InnerSortLimit.
//
// Fixture (shared with setop_paren_assoc_test.go):
//
//	a: 1,2,3   b: 2   c: 3   d: 1,3   e: 2,4   f: 4   g: 2,3   h: 9
//
// Every `want` below was captured from PostgreSQL 18.3 (the read-only oracle,
// port 65438, temp tables with the same contents) running the identical
// statement, not derived from goopg.

import "testing"

// TestSetOpTreeInnerAndOuterClausesCoexist is the acceptance matrix for the two
// shapes the linked list could not represent, plus the neighbours that prove
// the fix did not just move the collision somewhere else.
//
// Assertions are ORDERED (orderedInts, not sortedInts): what these cases test
// is precisely which node a sort or a limit ended up on, and a sorted-multiset
// comparison cannot see a sort applied to the wrong operand.
func TestSetOpTreeInnerAndOuterClausesCoexist(t *testing.T) {
	ctx, cleanup := setOpAssocFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		want []int64
		why  string
		// unordered marks a case whose output order SQL leaves unspecified
		// (no trailing ORDER BY); compare as a multiset instead. Everything
		// else is compared in order, because the point under test is which
		// node a sort or a limit landed on.
		unordered bool
	}{
		{
			// The first filed shape. Inner: {1,2,3} sorted LIMIT 2 = {1,2}.
			// Outer: UNION ALL {9} ordered DESC → 9,2,1. Before the tree, the
			// outer ORDER BY had to take the head branch's slot (losing the
			// inner limit) or be dropped (losing the ordering) — goopg dropped
			// it and returned the three rows unordered.
			name: "inner_sort_limit_plus_outer_desc",
			sql:  "(SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL (SELECT x FROM h) ORDER BY 1 DESC",
			want: []int64{9, 2, 1},
			why:  "outer ORDER BY orders the union; inner LIMIT still selects {1,2}",
		},
		{
			// The second filed shape. Inner LIMIT 2 → {1,2}; UNION ALL {9};
			// outer LIMIT 1 → the first row. goopg used to overwrite the inner
			// LIMIT 2 with the outer LIMIT 1 (the "outer clause wins" rule
			// M0125-0017 had to adopt), which gave {1} for the wrong reason and
			// {1,2,9} truncated differently for any other pair of limits.
			name: "inner_limit_plus_outer_limit",
			sql:  "((SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL SELECT x FROM h) LIMIT 1",
			want: []int64{1},
			why:  "both limits apply, at their own levels",
		},
		{
			// Same nesting, but the outer sort makes the two limits separable:
			// {1,2} UNION ALL {9} = {1,2,9}, DESC → 9,2,1, LIMIT 2 → 9,2. An
			// inner limit lost to the outer one would give a 1-row answer.
			name: "inner_limit_plus_outer_sort_limit",
			sql:  "((SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL SELECT x FROM h) ORDER BY 1 DESC LIMIT 2",
			want: []int64{9, 2},
			why:  "outer ORDER BY + LIMIT over an inner-limited union",
		},
		{
			// A BARE right branch's trailing ORDER BY belongs to the whole
			// union. Under the linked list the head branch owned the slot, so
			// the sort stayed on the right branch and the ordering was lost —
			// a deferral-ledger row (2026-07-29) this closes.
			name: "inner_limit_bare_right_outer_orderby",
			sql:  "(SELECT x FROM a LIMIT 2) UNION ALL SELECT x FROM g ORDER BY 1",
			want: []int64{1, 2, 2, 3},
			why:  "trailing ORDER BY on a bare right branch orders the union",
		},
		{
			// The inner branch's DESC decides WHICH rows survive its LIMIT
			// ({3,2}), and the outer ASC then orders the union: 2,3,9. If the
			// outer sort had reached the inner branch, the surviving rows
			// would be {1,2} and the answer 1,2,9.
			name: "inner_desc_limit_plus_outer_asc",
			sql:  "(SELECT x FROM a ORDER BY 1 DESC LIMIT 2) UNION ALL (SELECT x FROM h) ORDER BY 1",
			want: []int64{2, 3, 9},
			why:  "inner and outer sorts run in opposite directions",
		},
		{
			// Each parenthesised branch keeps its own LIMIT, and the trailing
			// ORDER BY still orders the union.
			name: "both_branches_limited_then_outer_desc",
			sql:  "((SELECT x FROM a LIMIT 1) UNION ALL (SELECT x FROM g LIMIT 1)) ORDER BY 1 DESC",
			want: []int64{2, 1},
			why:  "two independent inner limits under one outer sort",
		},
		{
			// A grouping node is a leaf operand, so the EXCEPT after the ')'
			// cannot reach into the union: ({1,2} UNION ALL {9}) EXCEPT {2,3}
			// = {1,9}. No trailing ORDER BY, so only the multiset is
			// contracted — PG happens to print 9,1 and goopg 1,9.
			name:      "grouped_union_then_except",
			sql:       "((SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL SELECT x FROM h) EXCEPT SELECT x FROM g",
			want:      []int64{1, 9},
			why:       "the grouping node is atomic for the following operator",
			unordered: true,
		},
		{
			// OFFSET written after the ')' skips a row of the UNION, not of the
			// inner branch: {1,2,9} OFFSET 1 = {2,9}.
			name: "outer_offset_over_inner_limit",
			sql:  "((SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL SELECT x FROM h) OFFSET 1",
			want: []int64{2, 9},
			why:  "outer OFFSET applies to the union",
		},
		{
			// Control: precedence across parenthesised operands is unchanged
			// (M0125-0016). INTERSECT binds tighter, so this is
			// {1,2,3} UNION ({2,3} INTERSECT {3}) = {1,2,3}.
			name: "control_precedence_across_parens",
			sql:  "(SELECT x FROM a) UNION (SELECT x FROM g) INTERSECT (SELECT x FROM c) ORDER BY 1",
			want: []int64{1, 2, 3},
			why:  "INTERSECT still binds tighter than UNION across ')' boundaries",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			got := orderedInts(t, rows)
			if c.unordered {
				got = sortedInts(t, rows)
			}
			if !eqInts(got, c.want) {
				t.Errorf("%s\n  got  %v\n  want %v (PostgreSQL 18.3) — %s",
					c.sql, got, c.want, c.why)
			}
		})
	}
}

// TestSetOpTreeGroupingNodeInSubqueryContexts pins that a grouping node is a
// usable query wherever a parenthesised set-op chain may appear. The node has
// no target list of its own, so every consumer that names a query's output
// columns — derived tables, CTEs, IN/EXISTS operands, INSERT sources — has to
// descend to the leftmost branch below it (analyzer.setOpLeftmostBranch).
// Without that descent these report `42703 column "x" does not exist`.
func TestSetOpTreeGroupingNodeInSubqueryContexts(t *testing.T) {
	ctx, cleanup := setOpAssocFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		want []int64
	}{
		{
			name: "derived_table",
			sql: "SELECT x FROM ((SELECT x FROM a ORDER BY 1 LIMIT 2) " +
				"UNION ALL SELECT x FROM h) q ORDER BY 1",
			want: []int64{1, 2, 9},
		},
		{
			name: "cte_body",
			sql: "WITH w AS ((SELECT x FROM a ORDER BY 1 LIMIT 2) " +
				"UNION ALL SELECT x FROM h) SELECT x FROM w ORDER BY 1",
			want: []int64{1, 2, 9},
		},
		{
			name: "in_operand",
			sql: "SELECT x FROM a WHERE x IN ((SELECT x FROM b) " +
				"UNION (SELECT x FROM c)) ORDER BY 1",
			want: []int64{2, 3},
		},
		{
			name: "scalar_subquery_source",
			sql: "SELECT (SELECT max(x) FROM ((SELECT x FROM a LIMIT 2) " +
				"UNION ALL SELECT x FROM h) s)",
			want: []int64{9},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := orderedInts(t, runQuery(t, ctx, c.sql))
			if !eqInts(got, c.want) {
				t.Errorf("%s\n  got  %v\n  want %v (PostgreSQL 18.3)", c.sql, got, c.want)
			}
		})
	}
}
