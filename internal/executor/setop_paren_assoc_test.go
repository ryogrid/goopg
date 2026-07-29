package executor

// setop_paren_assoc_test.go — M0125-0006, accepted BY VALUE.
//
// SQL and PostgreSQL associate equal-precedence set operators LEFT to right.
// goopg did so only while the branches were bare: `parseParenthesisedSelectStmt`
// marked a branch `Parenthesized` at its closing ')' and then absorbed a
// set-operator written after that ')' into the same chain, so the planner's
// flattening loop treated the extended chain as one atomic operand and planned
// `A EXCEPT (B EXCEPT C)` for `(A) EXCEPT (B) EXCEPT (C)`.
//
// This is a WRONG-ANSWER defect that row counts cannot see — TPC-DS Q87
// returned 47218 against PG's 47049 while both returned exactly one row, so the
// SF0.5 oracle, the nightly row anchors and the harness comparison were all
// structurally blind to it. Every case below is therefore asserted by value.
//
// UNION-only and INTERSECT-only chains are unaffected *only* because those
// operators are associative; the controls here exist so that their passing is
// not mistaken for coverage.
//
// Expected values were taken from PostgreSQL 18.3 (the read-only oracle), not
// derived from goopg.

import (
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// setOpAssocFixture builds the small integer sets the associativity matrix
// needs. They are chosen so left- and right-association give DIFFERENT answers
// for every non-control case — a fixture where both groupings agree would make
// the test vacuous.
//
//	a: 1,2,3   b: 2   c: 3   d: 1,3   e: 2,4   f: 4   g: 2,3   h: 9
func setOpAssocFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	tables := map[string][]int64{
		"a": {1, 2, 3},
		"b": {2},
		"c": {3},
		"d": {1, 3},
		"e": {2, 4},
		"f": {4},
		"g": {2, 3},
		"h": {9},
	}
	names := make([]string, 0, len(tables))
	for n := range tables {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic creation order
	for _, n := range names {
		if err := runDDL(t, ctx, "CREATE TABLE "+n+" (x int)"); err != nil {
			cleanup()
			t.Fatalf("CREATE TABLE %s: %v", n, err)
		}
		tb, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: n})
		rel := ctx.Catalog.RelFileNode(tb)
		for _, v := range tables[n] {
			if err := writeHeapRow(ctx, rel, tb.Columns, Row{{Kind: KindInt, Int: v}}); err != nil {
				cleanup()
				t.Fatalf("load %s=%d: %v", n, v, err)
			}
		}
	}
	return ctx, cleanup
}

// sortedInts flattens single-column int rows into a sorted slice, so a test
// failure reports the actual set rather than a row count.
func sortedInts(t *testing.T, rows []Row) []int64 {
	t.Helper()
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		if len(r) != 1 || r[0].Kind != KindInt {
			t.Fatalf("unexpected row shape %+v", r)
		}
		out = append(out, r[0].Int)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func eqInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSetOpParenthesisedChainAssociativity is the acceptance matrix. Every
// `want` is PostgreSQL 18.3's answer for the identical statement.
func TestSetOpParenthesisedChainAssociativity(t *testing.T) {
	ctx, cleanup := setOpAssocFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		want []int64
	}{
		// --- the three confirmed-wrong forms -------------------------------
		{
			// Left: ({1,2,3} EXCEPT {2}) EXCEPT {3} = {1}.
			// goopg planned {1,2,3} EXCEPT ({2} EXCEPT {3}) = {1,3}.
			name: "except_chain_parenthesised_leaves",
			sql:  "(SELECT x FROM a) EXCEPT (SELECT x FROM b) EXCEPT (SELECT x FROM c)",
			want: []int64{1},
		},
		{
			name: "except_all_chain_parenthesised_leaves",
			sql:  "(SELECT x FROM a) EXCEPT ALL (SELECT x FROM b) EXCEPT ALL (SELECT x FROM c)",
			want: []int64{1},
		},
		{
			// Left: ({1,3} UNION {2}) EXCEPT {3} = {1,2}.
			// goopg planned {1,3} UNION ({2} EXCEPT {3}) = {1,2,3}.
			name: "mixed_union_then_except",
			sql:  "(SELECT x FROM d) UNION (SELECT x FROM b) EXCEPT (SELECT x FROM c)",
			want: []int64{1, 2},
		},

		// --- same defect reached without parenthesising the FIRST branch ---
		{
			name: "bare_first_branch",
			sql:  "SELECT x FROM a EXCEPT (SELECT x FROM b) EXCEPT (SELECT x FROM c)",
			want: []int64{1},
		},

		// --- the parenthesised operand is a COMPOUND, not a leaf -----------
		{
			// ({1,2,3} EXCEPT ({2,4} EXCEPT {4})) EXCEPT {3} = {1}.
			// The parens cover 2 branches; the chain continues past them.
			name: "compound_operand_then_trailing_op",
			sql: "SELECT x FROM a EXCEPT (SELECT x FROM e EXCEPT SELECT x FROM f) " +
				"EXCEPT SELECT x FROM c",
			want: []int64{1},
		},
		{
			// ({3} UNION ({2,3} EXCEPT {3})) UNION {9} = {2,3,9}. This is the
			// case that proves a bool cannot express the boundary: simply
			// clearing Parenthesized here would flatten the whole chain to
			// (({3} UNION {2,3}) EXCEPT {3}) UNION {9} = {2,9} and lose the
			// user's grouping.
			name: "compound_operand_grouping_preserved",
			sql: "SELECT x FROM c UNION (SELECT x FROM g EXCEPT SELECT x FROM c) " +
				"UNION SELECT x FROM h",
			want: []int64{2, 3, 9},
		},

		// --- explicit RIGHT grouping must still be honoured ----------------
		{
			// {1,2,3} EXCEPT ({2} EXCEPT {2}) = {1,2,3} EXCEPT {} = {1,2,3}.
			// Nothing follows the outer ')', so the operand stays atomic.
			name: "fully_parenthesised_right_operand_stays_atomic",
			sql:  "SELECT x FROM a EXCEPT ((SELECT x FROM b) EXCEPT (SELECT x FROM b))",
			want: []int64{1, 2, 3},
		},
		{
			// The outer ')' covers the EXCEPT the inner level absorbed, so the
			// boundary recorded by that inner level must be reset.
			name: "nested_parens_reset_boundary",
			sql:  "(SELECT x FROM a) EXCEPT ((SELECT x FROM b) EXCEPT (SELECT x FROM b))",
			want: []int64{1, 2, 3},
		},
		{
			name: "triple_nested_leading_parens",
			sql:  "(((SELECT x FROM a))) EXCEPT (SELECT x FROM b) EXCEPT (SELECT x FROM c)",
			want: []int64{1},
		},

		// --- longer chains --------------------------------------------------
		{
			// ((({1,2,3} EXCEPT {2}) EXCEPT {3}) EXCEPT {4}) = {1}.
			name: "four_way_except_chain",
			sql: "(SELECT x FROM a) EXCEPT (SELECT x FROM b) EXCEPT (SELECT x FROM c) " +
				"EXCEPT (SELECT x FROM f)",
			want: []int64{1},
		},

		// --- INTERSECT precedence: must NOT regress -------------------------
		{
			// PG gram.y:825-826 declares INTERSECT tighter than UNION/EXCEPT,
			// so this is {1} UNION ({2,3} INTERSECT {3}) = {1,3}, NOT
			// ({1} UNION {2,3}) INTERSECT {3} = {3}. The paren-boundary cut
			// must decline when the operator after the ')' binds tighter.
			name: "intersect_binds_tighter_than_union",
			sql:  "(SELECT x FROM d) UNION (SELECT x FROM g) INTERSECT (SELECT x FROM c)",
			want: []int64{1, 3},
		},
		{
			// ({1,2,3} INTERSECT {2,3}) EXCEPT {3} = {2}: here the trailing
			// operator binds LOOSER, so the cut is correct.
			name: "intersect_then_looser_except",
			sql:  "(SELECT x FROM a) INTERSECT (SELECT x FROM g) EXCEPT (SELECT x FROM c)",
			want: []int64{2},
		},

		// --- controls: associative operators, unaffected by the defect ------
		{
			name: "control_union_only_chain",
			sql:  "(SELECT x FROM d) UNION (SELECT x FROM b) UNION (SELECT x FROM c)",
			want: []int64{1, 2, 3},
		},
		{
			name: "control_intersect_only_chain",
			sql:  "(SELECT x FROM a) INTERSECT (SELECT x FROM g) INTERSECT (SELECT x FROM c)",
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

// TestSetOpParenChainInSubqueryContexts covers the sibling paths that embed a
// set-op chain, per Hard-won Rule #2. The derived-table case is the exact shape
// of TPC-DS Q87 (`select count(*) from ((A) except (B) except (C)) cool_cust`),
// which is what made this defect visible at all.
func TestSetOpParenChainInSubqueryContexts(t *testing.T) {
	ctx, cleanup := setOpAssocFixture(t)
	defer cleanup()

	chain := "(SELECT x FROM a) EXCEPT (SELECT x FROM b) EXCEPT (SELECT x FROM c)"
	cases := []struct {
		name string
		sql  string
		want []int64
	}{
		{"derived_table_q87_shape", "SELECT x FROM (" + chain + ") cool_cust", []int64{1}},
		{"cte_body", "WITH w AS (" + chain + ") SELECT x FROM w", []int64{1}},
		{"scalar_subquery", "SELECT (" + chain + ")", []int64{1}},
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
