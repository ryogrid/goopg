package parser

// setop_paren_assoc_test.go — M0125-0006.
//
// `parseParenthesisedSelectStmt` marks a SelectStmt `Parenthesized` at the
// closing ')' and then greedily absorbs a set-operator written AFTER that ')'
// into the same chain. The flag therefore claimed the user's parentheses
// covered an operator that appeared outside them, and the planner's
// left-associativity flattening stopped early on it — planning
// `A EXCEPT (B EXCEPT C)` for `(A) EXCEPT (B) EXCEPT (C)`.
//
// The parentheses wrap a *prefix* of the resulting chain, which a bool cannot
// express, so the parser now also records ParenBranches: how many branches were
// really inside them. These tests pin that number, because it is the only
// record of where the ')' stood once the chain has been flattened.
//
// PostgreSQL needs no such field — `select_with_parens` is a leaf operand in
// gram.y, so transformSetOperationStmt() always receives a left-deep tree.

import "testing"

// chainOf returns the SelectStmt chain rooted at s, walking SetOp.Right.
func chainOf(s *SelectStmt) []*SelectStmt {
	out := []*SelectStmt{s}
	for cur := s; cur.SetOp != nil; cur = cur.SetOp.Right {
		out = append(out, cur.SetOp.Right)
	}
	return out
}

func parseOneSelect(t *testing.T, sql string) *SelectStmt {
	t.Helper()
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse(%q): got %d stmts, want 1", sql, len(stmts))
	}
	sel, ok := stmts[0].(*SelectStmt)
	if !ok {
		t.Fatalf("Parse(%q): got %T, want *SelectStmt", sql, stmts[0])
	}
	return sel
}

// TestParenBranchesRecordsParenBoundary pins ParenBranches for every shape the
// planner has to distinguish. The value is "branches inside the parentheses",
// and 0 means "the parentheses covered this node's whole chain".
func TestParenBranchesRecordsParenBoundary(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		// want[i] is the expected ParenBranches of chain node i; -1 means
		// "node is not Parenthesized at all".
		want []int
	}{
		{
			// The defect's headline form. Node 1 (B) is parenthesised but
			// absorbed `EXCEPT (C)` from after its ')', so exactly 1 branch
			// was inside. Node 2 (C) absorbed nothing, so 0.
			name: "leaf_parens_then_trailing_op",
			sql:  "(SELECT 1) EXCEPT (SELECT 2) EXCEPT (SELECT 3)",
			want: []int{1, 1, 0},
		},
		{
			name: "except_all_variant",
			sql:  "(SELECT 1) EXCEPT ALL (SELECT 2) EXCEPT ALL (SELECT 3)",
			want: []int{1, 1, 0},
		},
		{
			name: "mixed_union_then_except",
			sql:  "(SELECT 1) UNION (SELECT 2) EXCEPT (SELECT 3)",
			want: []int{1, 1, 0},
		},
		{
			// Bare leftmost branch: the head is not parenthesised at all,
			// but the right operand still absorbed a trailing operator.
			name: "bare_first_branch",
			sql:  "SELECT 1 EXCEPT (SELECT 2) EXCEPT (SELECT 3)",
			want: []int{-1, 1, 0},
		},
		{
			// A parenthesised COMPOUND that then absorbs a trailing operator:
			// 2 branches (1 link) were inside the parens.
			name: "compound_parens_then_trailing_op",
			sql:  "SELECT 1 EXCEPT (SELECT 2 EXCEPT SELECT 3) EXCEPT SELECT 4",
			want: []int{-1, 2, -1, -1},
		},
		{
			// Nothing follows the ')', so the parentheses cover the whole
			// chain and the operand is atomic: ParenBranches stays 0.
			name: "fully_parenthesised_right_operand",
			sql:  "SELECT 1 EXCEPT ((SELECT 2) EXCEPT (SELECT 3))",
			want: []int{-1, 0, 0},
		},
		{
			// The outer ')' of `((B) EXCEPT (C))` really does cover the
			// EXCEPT that the inner level absorbed, so the reset at the
			// closing paren must clear the inner level's ParenBranches=1.
			// Without that reset this node would report 1 and the planner
			// would wrongly re-flatten an explicitly grouped operand.
			name: "nested_parens_reset_boundary",
			sql:  "(SELECT 1) EXCEPT ((SELECT 2) EXCEPT (SELECT 3))",
			want: []int{1, 0, 0},
		},
		{
			name: "triple_nested_leading_parens",
			sql:  "(((SELECT 1))) EXCEPT (SELECT 2) EXCEPT (SELECT 3)",
			want: []int{1, 1, 0},
		},
		{
			name: "four_way_chain",
			sql:  "(SELECT 1) EXCEPT (SELECT 2) EXCEPT (SELECT 3) EXCEPT (SELECT 4)",
			want: []int{1, 1, 1, 0},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chain := chainOf(parseOneSelect(t, c.sql))
			if len(chain) != len(c.want) {
				t.Fatalf("%s: chain length %d, want %d", c.sql, len(chain), len(c.want))
			}
			for i, w := range c.want {
				n := chain[i]
				if w < 0 {
					if n.Parenthesized {
						t.Errorf("node %d: Parenthesized=true, want false", i)
					}
					continue
				}
				if !n.Parenthesized {
					t.Errorf("node %d: Parenthesized=false, want true", i)
					continue
				}
				if n.ParenBranches != w {
					t.Errorf("node %d: ParenBranches=%d, want %d", i, n.ParenBranches, w)
				}
			}
		})
	}
}

// TestParenBranchesZeroWhenNoTrailingOp guards the invariant the planner
// depends on: a parenthesised operand with nothing after its ')' must report
// ParenBranches == 0, so parenBoundary() keeps treating it as atomic. A
// non-zero value here would silently re-flatten user-written grouping.
func TestParenBranchesZeroWhenNoTrailingOp(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1 UNION (SELECT 2 UNION ALL SELECT 2)",
		"SELECT 1 EXCEPT (SELECT 2 EXCEPT SELECT 3)",
		"SELECT 1 UNION ((SELECT 2 INTERSECT SELECT 3))",
	} {
		t.Run(sql, func(t *testing.T) {
			s := parseOneSelect(t, sql)
			if s.SetOp == nil {
				t.Fatalf("%s: no SetOp", sql)
			}
			right := s.SetOp.Right
			if !right.Parenthesized {
				t.Fatalf("%s: right operand not marked Parenthesized", sql)
			}
			if right.ParenBranches != 0 {
				t.Errorf("%s: ParenBranches=%d, want 0 (nothing follows the ')')",
					sql, right.ParenBranches)
			}
		})
	}
}

// TestParenthesizedFlagUnaffectedForInsertSource pins that the INSERT source
// disambiguation (`INSERT INTO t (SELECT …)`, which reuses Parenthesized to
// tell a query source from a column list) is untouched: no set-operator
// follows the ')', so ParenBranches stays 0 and the flag stays set.
func TestParenthesizedFlagUnaffectedForInsertSource(t *testing.T) {
	stmts, err := Parse("INSERT INTO t (SELECT x FROM s UNION SELECT y FROM u)")
	if err != nil {
		t.Fatal(err)
	}
	ins, ok := stmts[0].(*InsertStmt)
	if !ok {
		t.Fatalf("got %T, want *InsertStmt", stmts[0])
	}
	if ins.Select == nil || !ins.Select.Parenthesized {
		t.Fatalf("Select=%+v, want a parenthesised SelectStmt", ins.Select)
	}
	if ins.Select.ParenBranches != 0 {
		t.Errorf("ParenBranches=%d, want 0", ins.Select.ParenBranches)
	}
}

// TestInnerSortLimitMarksTheOwningBoundary pins the carrier M0125-0017 added.
//
// InnerSegmentCount alone could not say "the parentheses closed after the FIRST
// branch", because 0 is its unset sentinel. InnerSortLimit supplies the missing
// bit: it is true exactly when the ORDER BY / LIMIT / OFFSET on the head node
// were written inside the parentheses, and InnerSegmentCount then says how many
// set-op segments they cover — possibly zero.
func TestInnerSortLimitMarksTheOwningBoundary(t *testing.T) {
	cases := []struct {
		sql       string
		wantFlag  bool
		wantCount int
		why       string
	}{
		{
			sql:       "(SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL SELECT x FROM h",
			wantFlag:  true,
			wantCount: 0,
			why:       "single parenthesised head branch: boundary at segment 0",
		},
		{
			sql:       "(SELECT x FROM a LIMIT 2) UNION ALL SELECT x FROM h",
			wantFlag:  true,
			wantCount: 0,
			why:       "LIMIT alone is enough to own the boundary",
		},
		{
			sql:       "((SELECT x FROM a ORDER BY 1 LIMIT 2)) UNION ALL SELECT x FROM h",
			wantFlag:  true,
			wantCount: 0,
			why:       "redundant parentheses do not move the boundary",
		},
		{
			sql:       "((SELECT x FROM a INTERSECT SELECT x FROM g ORDER BY 1)) UNION ALL SELECT x FROM h",
			wantFlag:  true,
			wantCount: 1,
			why:       "M0097-0044's shape: the parens covered one INTERSECT segment",
		},
		{
			sql:       "((SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL SELECT x FROM h) EXCEPT SELECT x FROM g",
			wantFlag:  true,
			wantCount: 0,
			why:       "an outer paren level must not widen a boundary an inner level claimed",
		},
		{
			sql:       "(SELECT x FROM a) UNION ALL (SELECT x FROM h) ORDER BY 1 LIMIT 2",
			wantFlag:  false,
			wantCount: 0,
			why:       "written after the ')': belongs to the whole chain",
		},
		{
			sql:       "((SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL SELECT x FROM h) LIMIT 1",
			wantFlag:  false,
			wantCount: 0,
			why:       "trailing clause collides with the head's slot; the outer clause wins",
		},
	}
	for _, c := range cases {
		t.Run(c.why, func(t *testing.T) {
			stmts, err := Parse(c.sql)
			if err != nil {
				t.Fatalf("%s: %v", c.sql, err)
			}
			sel, ok := stmts[0].(*SelectStmt)
			if !ok {
				t.Fatalf("got %T, want *SelectStmt", stmts[0])
			}
			if sel.InnerSortLimit != c.wantFlag || sel.InnerSegmentCount != c.wantCount {
				t.Errorf("%s\n  got  InnerSortLimit=%v InnerSegmentCount=%d\n  want %v / %d (%s)",
					c.sql, sel.InnerSortLimit, sel.InnerSegmentCount,
					c.wantFlag, c.wantCount, c.why)
			}
		})
	}
}
