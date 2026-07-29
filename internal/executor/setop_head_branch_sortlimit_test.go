package executor

// setop_head_branch_sortlimit_test.go — M0125-0017, accepted BY VALUE.
//
// PostgreSQL's grammar makes `select_with_parens` a leaf operand, so a sort or
// limit written INSIDE the parentheses belongs to that branch alone:
//
//	simple_select: ...
//	select_with_parens: '(' select_no_parens ')'
//	select_no_parens: select_clause opt_sort_clause ... opt_select_limit
//
// (postgres/src/backend/parser/gram.y). `(A ORDER BY 1 LIMIT 2) UNION ALL (C)`
// therefore limits A and then unions C onto the two surviving rows.
//
// goopg stores a set-op chain as a linked list, so the parenthesised head
// branch and the whole compound share ONE SelectStmt — and with it one
// OrderBy/Limit/Offset slot. `parseParenthesisedSelectStmt` recorded which
// segments the parentheses covered only when the parenthesised content was
// already a compound (`InnerSegmentCount`, M0097-0044), and its 0 value doubles
// as the "unset" sentinel, so the SINGLE-branch case could not be expressed at
// all: the head's LIMIT was applied to the union instead, and the entire
// UNION ALL branch vanished from the result.
//
// Like M0125-0006/-0016 this is a WRONG-ANSWER defect that returns a plausible
// row count, so every case is asserted by value. Fixture and helpers are shared
// with setop_paren_assoc_test.go:
//
//	a: 1,2,3   b: 2   c: 3   d: 1,3   e: 2,4   f: 4   g: 2,3   h: 9
//
// Every `want` below was captured from PostgreSQL 18.3 (the read-only oracle,
// port 65438) running the identical statement, not derived from goopg.

import "testing"

// orderedInts flattens single-column int rows WITHOUT sorting, so a case whose
// row ORDER is part of the contract (ORDER BY … DESC inside the parentheses)
// fails loudly instead of being normalised away by sortedInts.
func orderedInts(t *testing.T, rows []Row) []int64 {
	t.Helper()
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		if len(r) != 1 || r[0].Kind != KindInt {
			t.Fatalf("unexpected row shape %+v", r)
		}
		out = append(out, r[0].Int)
	}
	return out
}

// TestSetOpParenthesisedHeadBranchSortLimit is the acceptance matrix:
// {ORDER BY only, LIMIT only, both} x {parenthesised right branch, bare right
// branch}, plus the controls that must not move.
//
// Cases are compared as SORTED multisets: only the parenthesised head branch's
// row SELECTION is under test here, and a set operation's output order is
// unspecified in PostgreSQL without a trailing ORDER BY. The DESC case below
// (TestSetOpParenthesisedHeadBranchOrderIsTheBranchOrder) covers ordering.
func TestSetOpParenthesisedHeadBranchSortLimit(t *testing.T) {
	ctx, cleanup := setOpAssocFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		want []int64
	}{
		// --- ORDER BY + LIMIT inside the parentheses ------------------------
		{
			// {1,2,3} sorted, first 2 → {1,2}; then UNION ALL {9}.
			// goopg applied LIMIT 2 to the UNION and returned {1,2}: the
			// whole UNION ALL branch vanished.
			name: "both_paren_right",
			sql:  "(SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL (SELECT x FROM h)",
			want: []int64{1, 2, 9},
		},
		{
			// A bare right branch takes the same parse path for the head, so
			// it must give the same answer.
			name: "both_bare_right",
			sql:  "(SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL SELECT x FROM h",
			want: []int64{1, 2, 9},
		},
		// --- LIMIT only (no sort) inside the parentheses --------------------
		// The fixture is loaded in insertion order and never updated, so a
		// seq-scan LIMIT 2 yields {1,2} on both engines. PostgreSQL leaves the
		// choice unspecified; the assertion pins goopg's agreement with the
		// oracle on THIS fixture, not a general ordering guarantee.
		{
			name: "limit_only_paren_right",
			sql:  "(SELECT x FROM a LIMIT 2) UNION ALL (SELECT x FROM h)",
			want: []int64{1, 2, 9},
		},
		{
			name: "limit_only_bare_right",
			sql:  "(SELECT x FROM a LIMIT 2) UNION ALL SELECT x FROM h",
			want: []int64{1, 2, 9},
		},
		// --- ORDER BY only inside the parentheses ---------------------------
		// Nothing is dropped, so these already passed; they are the controls
		// that prove the fix does not start discarding rows.
		{
			name: "orderby_only_paren_right",
			sql:  "(SELECT x FROM a ORDER BY 1) UNION ALL (SELECT x FROM h)",
			want: []int64{1, 2, 3, 9},
		},
		{
			name: "orderby_only_bare_right",
			sql:  "(SELECT x FROM a ORDER BY 1) UNION ALL SELECT x FROM h",
			want: []int64{1, 2, 3, 9},
		},
		// --- OFFSET inside the parentheses ----------------------------------
		{
			// {1,2,3} sorted, skip 1 → {2,3}; then UNION ALL {9}.
			name: "offset_inside_parens",
			sql:  "(SELECT x FROM a ORDER BY 1 OFFSET 1) UNION ALL (SELECT x FROM h)",
			want: []int64{2, 3, 9},
		},
		// --- the head branch's sort key need not be an output column --------
		{
			// PG evaluates `(SELECT x FROM a ORDER BY x*-1 LIMIT 2)` as a
			// plain SELECT, so it may sort by a non-output expression. Only a
			// fix that lets the head branch keep its OWN sort/limit (rather
			// than re-resolving it against the set-op output columns) can
			// answer this at all.
			name: "orderby_nonoutput_expr",
			sql:  "(SELECT x FROM a ORDER BY x*-1 LIMIT 2) UNION ALL SELECT x FROM h",
			want: []int64{2, 3, 9},
		},
		// --- extra parentheses around the head branch -----------------------
		{
			name: "nested_parens_head",
			sql:  "((SELECT x FROM a ORDER BY 1 LIMIT 2)) UNION ALL SELECT x FROM h",
			want: []int64{1, 2, 9},
		},
		// --- the head boundary is also a precedence barrier ------------------
		{
			// ({1,2,3} sorted LIMIT 2 = {1,2}) EXCEPT {2,3} = {1}.
			name: "head_limit_then_except",
			sql:  "(SELECT x FROM a ORDER BY 1 LIMIT 2) EXCEPT SELECT x FROM g",
			want: []int64{1},
		},
		{
			// {1,2} UNION ({9} INTERSECT {2,3}) = {1,2} UNION {} = {1,2}.
			// Exercises the head boundary together with M0125-0016's
			// INTERSECT-binds-tighter fold.
			name: "head_limit_then_intersect_chain",
			sql:  "(SELECT x FROM a ORDER BY 1 LIMIT 2) UNION SELECT x FROM h INTERSECT SELECT x FROM g",
			want: []int64{1, 2},
		},
		{
			// The right branch's own parenthesised ORDER BY/LIMIT stays on the
			// right branch: {1,2} UNION ALL ({2,3} sorted LIMIT 1 = {2}).
			name: "both_branches_own_limit",
			sql:  "(SELECT x FROM a LIMIT 2) UNION ALL (SELECT x FROM g ORDER BY 1 LIMIT 1)",
			want: []int64{1, 2, 2},
		},
		{
			// A BARE right branch's trailing ORDER BY belongs to the whole
			// union in PG. goopg cannot hold it — the single OrderBy slot is
			// the head branch's — so the sort stays on the right branch and
			// only the row SELECTION is guaranteed. {1,2} UNION ALL {2,3}.
			// The lost ordering is a deferral-ledger row (2026-07-29).
			name: "head_limit_bare_right_outer_orderby",
			sql:  "(SELECT x FROM a LIMIT 2) UNION ALL SELECT x FROM g ORDER BY 1",
			want: []int64{1, 2, 2, 3},
		},
		{
			// Same conflict with a parenthesised right branch: PG orders the
			// union 9,2,1; goopg returns the same three rows unordered.
			// Before M0125-0017 this returned {1,2} — a row was LOST, which is
			// strictly worse than an unspecified order.
			name: "head_limit_paren_right_outer_orderby",
			sql:  "(SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL (SELECT x FROM h) ORDER BY 1 DESC",
			want: []int64{1, 2, 9},
		},
		// --- controls: the sort/limit really is OUTSIDE the parentheses -----
		{
			// Written after the ')', so it binds to the whole UNION ALL.
			name: "control_outer_orderby_limit",
			sql:  "(SELECT x FROM a) UNION ALL (SELECT x FROM h) ORDER BY 1 LIMIT 2",
			want: []int64{1, 2},
		},
		{
			// M0097-0044's shape: the parenthesised content was ALREADY a
			// compound, so InnerSegmentCount (not the new head boundary)
			// carries it. ({1,2,3} INTERSECT {2,3}) = {2,3}, LIMIT 1 → {2};
			// then UNION ALL {9}.
			name: "control_inner_compound_boundary",
			sql:  "((SELECT x FROM a INTERSECT SELECT x FROM g ORDER BY 1 LIMIT 1)) UNION ALL SELECT x FROM h",
			want: []int64{2, 9},
		},
		{
			// No sort/limit anywhere inside: plain left-associative chain.
			name: "control_plain_paren_chain",
			sql:  "(SELECT x FROM a) EXCEPT (SELECT x FROM b) EXCEPT (SELECT x FROM c)",
			want: []int64{1},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sortedInts(t, runQuery(t, ctx, c.sql))
			if !eqInts(got, c.want) {
				t.Errorf("%s\n  got  %v\n  want %v (PostgreSQL 18.3)", c.sql, got, c.want)
			}
		})
	}
}

// TestSetOpParenthesisedHeadBranchOrderIsTheBranchOrder pins the ORDERING half
// of the contract: the head branch's ORDER BY decides WHICH rows survive its
// LIMIT, and a sorted-multiset assertion cannot see a DESC sort that was
// silently applied to the wrong operand.
func TestSetOpParenthesisedHeadBranchOrderIsTheBranchOrder(t *testing.T) {
	ctx, cleanup := setOpAssocFixture(t)
	defer cleanup()

	// PG: {1,2,3} DESC LIMIT 2 = {3,2}, then UNION ALL {9} → 3,2,9.
	// A DESC sort applied to the UNION instead would give 9,3 (and drop the
	// third row entirely), so the row VALUES alone already separate the two.
	const sql = "(SELECT x FROM a ORDER BY 1 DESC LIMIT 2) UNION ALL (SELECT x FROM h)"
	got := orderedInts(t, runQuery(t, ctx, sql))
	want := []int64{3, 2, 9}
	if !eqInts(got, want) {
		t.Errorf("%s\n  got  %v\n  want %v (PostgreSQL 18.3)", sql, got, want)
	}
}
