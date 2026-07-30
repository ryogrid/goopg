package executor

// setop_precedence_test.go — M0125-0016, accepted BY VALUE.
//
// PostgreSQL's grammar declares set-operator precedence as
//
//	%left		UNION EXCEPT
//	%left		INTERSECT
//
// (postgres/src/backend/parser/gram.y:825-826 — the later declaration binds
// tighter), so INTERSECT groups before UNION/EXCEPT and each level is
// left-associative.
//
// goopg's planner folded the flattened segment list left-deep regardless of the
// operator, so with NO PARENTHESES ANYWHERE `A UNION B INTERSECT C` planned as
// `(A UNION B) INTERSECT C`. M0125-0006 added a precedence guard at the paren
// boundary only (setOpBindsTighter), which made the explicitly-parenthesised
// spelling correct while the bare spelling stayed wrong. This matrix pins the
// bare spelling.
//
// Like M0125-0006 this is a WRONG-ANSWER defect that returns a plausible
// row count, so every case is asserted by value. Fixture and helpers are shared
// with setop_paren_assoc_test.go:
//
//	a: 1,2,3   b: 2   c: 3   d: 1,3   e: 2,4   f: 4   g: 2,3   h: 9
//
// Every `want` below was captured from PostgreSQL 18.3 (the read-only oracle)
// running the identical statement, not derived from goopg.

import "testing"

// TestSetOpBareChainPrecedence is the acceptance matrix: bare chains only, in
// both precedence directions (INTERSECT before and after the looser operator),
// plus controls where precedence and a left-deep fold agree.
func TestSetOpBareChainPrecedence(t *testing.T) {
	ctx, cleanup := setOpAssocFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		want []int64
	}{
		// --- INTERSECT to the RIGHT of a looser operator: it must group with
		// what FOLLOWS, so the left-deep fold is wrong ----------------------
		{
			// {1,3} UNION ({2,3} INTERSECT {3}) = {1,3}.
			// goopg planned ({1,3} UNION {2,3}) INTERSECT {3} = {3}.
			name: "union_then_intersect",
			sql:  "SELECT x FROM d UNION SELECT x FROM g INTERSECT SELECT x FROM c",
			want: []int64{1, 3},
		},
		{
			// {1,3} EXCEPT ({2,3} INTERSECT {3}) = {1}.
			// Left-deep would be ({1,3} EXCEPT {2,3}) INTERSECT {3} = {}.
			// EXCEPT ties with UNION, so it must behave identically.
			name: "except_then_intersect",
			sql:  "SELECT x FROM d EXCEPT SELECT x FROM g INTERSECT SELECT x FROM c",
			want: []int64{1},
		},
		{
			// {1,2,3} EXCEPT ({2} INTERSECT {3}) = {1,2,3} EXCEPT {} = {1,2,3}.
			// Left-deep: ({1,2,3} EXCEPT {2}) INTERSECT {3} = {3}. An empty
			// INTERSECT result makes the two groupings maximally far apart.
			name: "except_then_empty_intersect",
			sql:  "SELECT x FROM a EXCEPT SELECT x FROM b INTERSECT SELECT x FROM c",
			want: []int64{1, 2, 3},
		},
		{
			// ALL variants take the same precedence path; the operator's ALL
			// flag is carried per segment, not per level.
			// {1,3} UNION ALL ({2,3} INTERSECT {3}) = {1,3,3}.
			name: "union_all_then_intersect",
			sql:  "SELECT x FROM d UNION ALL SELECT x FROM g INTERSECT SELECT x FROM c",
			want: []int64{1, 3, 3},
		},
		{
			// {1,3} UNION ({2,3} INTERSECT ALL {3}) = {1,3}.
			name: "union_then_intersect_all",
			sql:  "SELECT x FROM d UNION SELECT x FROM g INTERSECT ALL SELECT x FROM c",
			want: []int64{1, 3},
		},

		// --- the INTERSECT run is longer than one operand -------------------
		{
			// {1,3} UNION (({2,3} INTERSECT {1,2,3}) INTERSECT {3}) = {1,3}.
			// Left-deep: (({1,3} UNION {2,3}) INTERSECT {1,2,3}) INTERSECT {3}
			// = {3}. A maximal run must be folded as one operand, not just its
			// first link.
			name: "union_then_intersect_run_of_two",
			sql: "SELECT x FROM d UNION SELECT x FROM g INTERSECT SELECT x FROM a " +
				"INTERSECT SELECT x FROM c",
			want: []int64{1, 3},
		},

		// --- the chain continues past the INTERSECT run ---------------------
		{
			// {1,2,3} UNION ({2} INTERSECT {3}) UNION {9} = {1,2,3,9}.
			// Left-deep: (({1,2,3} UNION {2}) INTERSECT {3}) UNION {9} = {3,9}.
			name: "union_intersect_union",
			sql: "SELECT x FROM a UNION SELECT x FROM b INTERSECT SELECT x FROM c " +
				"UNION SELECT x FROM h",
			want: []int64{1, 2, 3, 9},
		},
		{
			// ({9} UNION ({1,3} INTERSECT {2,3})) UNION {4} = {3,4,9}.
			// Left-deep: (({9} UNION {1,3}) INTERSECT {2,3}) UNION {4} = {3,4}.
			// The run sits in the MIDDLE of the chain, so the fold has to
			// resume the looser level after closing the group.
			name: "union_intersect_run_then_union",
			sql: "SELECT x FROM h UNION SELECT x FROM d INTERSECT SELECT x FROM g " +
				"UNION SELECT x FROM f",
			want: []int64{3, 4, 9},
		},

		// --- INTERSECT to the LEFT: precedence and a left-deep fold AGREE, so
		// these are non-regression controls, not new coverage ---------------
		{
			// ({1,2,3} INTERSECT {2,3}) UNION {3} = {2,3} either way.
			name: "control_intersect_then_union",
			sql:  "SELECT x FROM a INTERSECT SELECT x FROM g UNION SELECT x FROM c",
			want: []int64{2, 3},
		},
		{
			// ({1,2,3} INTERSECT {2,3}) EXCEPT {3} = {2} either way.
			name: "control_intersect_then_except",
			sql:  "SELECT x FROM a INTERSECT SELECT x FROM g EXCEPT SELECT x FROM c",
			want: []int64{2},
		},
		{
			// UNION and EXCEPT TIE, so this stays a plain left-deep fold:
			// ({1,3} UNION {2}) EXCEPT {3} = {1,2}. If the grouping pass ever
			// treated EXCEPT as tighter than UNION this would become
			// {1,3} UNION ({2} EXCEPT {3}) = {1,2,3}.
			name: "control_union_except_tie_left_assoc",
			sql:  "SELECT x FROM d UNION SELECT x FROM b EXCEPT SELECT x FROM c",
			want: []int64{1, 2},
		},
		{
			name: "control_intersect_only_chain",
			sql: "SELECT x FROM a INTERSECT SELECT x FROM g INTERSECT " +
				"SELECT x FROM c",
			want: []int64{3},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sortedInts(t, runQuery(t, ctx, c.sql))
			if !eqInts(got, c.want) {
				t.Errorf("%s\n got=%v\nwant=%v (PostgreSQL 18.3)", c.sql, got, c.want)
			}
		})
	}
}

// TestSetOpPrecedenceStopsAtParenBoundary pins the interaction between the new
// grouping pass and the two explicit-grouping mechanisms it must not reach
// across.
//
// SelectStmt.InnerSegmentCount (M0097-0044) marks a parenthesised head compound
// that carries its own ORDER BY/LIMIT. Those parentheses group the segments
// before the boundary explicitly, so an INTERSECT run written after the ')' may
// not absorb the branch immediately to its left — otherwise
// `(A UNION B ORDER BY 1) INTERSECT C` would silently become
// `A UNION (B INTERSECT C)`.
func TestSetOpPrecedenceStopsAtParenBoundary(t *testing.T) {
	ctx, cleanup := setOpAssocFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		want []int64
	}{
		{
			// ({1,3} UNION {2,3}) INTERSECT {3} = {3}.
			// Ignoring the barrier gives {1,3} UNION ({2,3} INTERSECT {3})
			// = {1,3} — the pre-parenthesised answer, which is wrong here
			// because the user grouped the UNION explicitly.
			name: "inner_orderby_group_is_a_precedence_barrier",
			sql: "(SELECT x FROM d UNION SELECT x FROM g ORDER BY 1) " +
				"INTERSECT SELECT x FROM c",
			want: []int64{3},
		},
		{
			// The LIMIT applies to the inner compound only:
			// ({1,2,3} UNION {9} ORDER BY 1 LIMIT 2) = {1,2}; ∩ {1,2,3} = {1,2}.
			name: "inner_orderby_limit_group_is_a_precedence_barrier",
			sql: "(SELECT x FROM a UNION SELECT x FROM h ORDER BY 1 LIMIT 2) " +
				"INTERSECT SELECT x FROM a",
			want: []int64{1, 2},
		},
		{
			// M0125-0006's boundary: the operand's parentheses close before the
			// INTERSECT, which binds tighter, so the chain is NOT cut there and
			// the tighter group forms: {1,3} UNION ({2,3} INTERSECT {3})
			// = {1,3}. Same value as the bare spelling above — that agreement
			// is the point of this fix.
			name: "paren_boundary_agrees_with_bare_chain",
			sql: "(SELECT x FROM d) UNION (SELECT x FROM g) " +
				"INTERSECT (SELECT x FROM c)",
			want: []int64{1, 3},
		},
		{
			// Explicit RIGHT grouping of the LOOSER operator must survive the
			// grouping pass: {1,2,3} INTERSECT ({2,3} UNION {4}) = {2,3}.
			// A precedence-only reading would give ({1,2,3} INTERSECT {2,3})
			// UNION {4} = {2,3,4}.
			name: "explicit_parens_override_precedence",
			sql:  "SELECT x FROM a INTERSECT (SELECT x FROM g UNION SELECT x FROM f)",
			want: []int64{2, 3},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sortedInts(t, runQuery(t, ctx, c.sql))
			if !eqInts(got, c.want) {
				t.Errorf("%s\n got=%v\nwant=%v (PostgreSQL 18.3)", c.sql, got, c.want)
			}
		})
	}
}
