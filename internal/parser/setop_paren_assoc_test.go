package parser

// setop_paren_assoc_test.go — M0125-0006, rewritten for M0125-0020.
//
// A set-op chain used to be a linked list: `parseParenthesisedSelectStmt`
// marked a SelectStmt `Parenthesized` at the closing ')' and then absorbed
// whatever followed that ')' — a set-operator, an ORDER BY, a LIMIT — into the
// SAME node. Head branch and whole compound therefore shared one chain and one
// sort/limit slot, and three annotations (ParenBranches, InnerSegmentCount,
// InnerSortLimit) existed only to describe the resulting overlap.
//
// The chain is now a TREE: text after the ')' attaches to a fresh GROUPING
// node whose SetOpOperand is the parenthesised query. That is what gram.y
// already says — `select_with_parens` is a leaf operand of `select_clause`, so
// transformSetOperationStmt() always receives a properly nested tree. These
// tests pin the shape, because it is the only record of where the ')' stood.

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

// nodeAt walks a path from s: "operand" descends into SetOpOperand (into the
// parenthesised branch), "right" follows SetOp.Right.
func nodeAt(t *testing.T, s *SelectStmt, path ...string) *SelectStmt {
	t.Helper()
	cur := s
	for i, step := range path {
		switch step {
		case "operand":
			if cur.SetOpOperand == nil {
				t.Fatalf("path %v: step %d (%s): node is not a grouping node", path, i, step)
			}
			cur = cur.SetOpOperand
		case "right":
			if cur.SetOp == nil {
				t.Fatalf("path %v: step %d (%s): node has no SetOp", path, i, step)
			}
			cur = cur.SetOp.Right
		default:
			t.Fatalf("unknown path step %q", step)
		}
	}
	return cur
}

// TestGroupingNodeWrapsParenthesisedOperand pins the tree shape for every form
// the planner has to distinguish. A node is a GROUPING node when text followed
// its ')' (that text lives on the grouping node, the branch lives below it);
// it is Parenthesized when the ')' closed with nothing after it, which is the
// planner's signal that the operand is atomic.
func TestGroupingNodeWrapsParenthesisedOperand(t *testing.T) {
	type node struct {
		group bool // SetOpOperand != nil
		paren bool // Parenthesized
	}
	cases := []struct {
		name string
		sql  string
		want []node // one entry per chain node, walking SetOp.Right
	}{
		{
			// The M0125-0006 headline form. Nodes 0 and 1 absorbed an EXCEPT
			// written after their ')', so each is a grouping node holding one
			// branch; node 2's ')' closed the statement, so it stays a plain
			// parenthesised leaf.
			name: "leaf_parens_then_trailing_op",
			sql:  "(SELECT 1) EXCEPT (SELECT 2) EXCEPT (SELECT 3)",
			want: []node{{group: true}, {group: true}, {paren: true}},
		},
		{
			name: "except_all_variant",
			sql:  "(SELECT 1) EXCEPT ALL (SELECT 2) EXCEPT ALL (SELECT 3)",
			want: []node{{group: true}, {group: true}, {paren: true}},
		},
		{
			name: "mixed_union_then_except",
			sql:  "(SELECT 1) UNION (SELECT 2) EXCEPT (SELECT 3)",
			want: []node{{group: true}, {group: true}, {paren: true}},
		},
		{
			// Bare leftmost branch: the head is neither parenthesised nor a
			// grouping node, but the right operand still absorbed a trailing
			// operator and so got one.
			name: "bare_first_branch",
			sql:  "SELECT 1 EXCEPT (SELECT 2) EXCEPT (SELECT 3)",
			want: []node{{}, {group: true}, {paren: true}},
		},
		{
			// A parenthesised COMPOUND that then absorbs a trailing operator:
			// the whole compound goes below the grouping node, so the outer
			// EXCEPT can never reach into it.
			name: "compound_parens_then_trailing_op",
			sql:  "SELECT 1 EXCEPT (SELECT 2 EXCEPT SELECT 3) EXCEPT SELECT 4",
			want: []node{{}, {group: true}, {}},
		},
		{
			// Nothing follows the ')', so the parentheses cover the whole
			// chain: no grouping node, and Parenthesized marks the operand
			// atomic for the planner.
			name: "fully_parenthesised_right_operand",
			sql:  "SELECT 1 EXCEPT ((SELECT 2) EXCEPT (SELECT 3))",
			want: []node{{}, {group: true, paren: true}, {paren: true}},
		},
		{
			// The outer ')' of `((B) EXCEPT (C))` really does cover the EXCEPT
			// the inner level absorbed, so the node carrying it is marked
			// Parenthesized and the planner stops flattening there. Under the
			// linked list this needed an explicit ParenBranches reset.
			name: "nested_parens_cover_inner_op",
			sql:  "(SELECT 1) EXCEPT ((SELECT 2) EXCEPT (SELECT 3))",
			want: []node{{group: true}, {group: true, paren: true}, {paren: true}},
		},
		{
			// Redundant wrapping adds no grouping node: only the outermost ')'
			// has text after it.
			name: "triple_nested_leading_parens",
			sql:  "(((SELECT 1))) EXCEPT (SELECT 2) EXCEPT (SELECT 3)",
			want: []node{{group: true}, {group: true}, {paren: true}},
		},
		{
			name: "four_way_chain",
			sql:  "(SELECT 1) EXCEPT (SELECT 2) EXCEPT (SELECT 3) EXCEPT (SELECT 4)",
			want: []node{{group: true}, {group: true}, {group: true}, {paren: true}},
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
				if got := n.SetOpOperand != nil; got != w.group {
					t.Errorf("node %d: grouping node=%v, want %v", i, got, w.group)
				}
				if n.Parenthesized != w.paren {
					t.Errorf("node %d: Parenthesized=%v, want %v", i, n.Parenthesized, w.paren)
				}
			}
		})
	}
}

// TestNoGroupingNodeWhenNothingFollowsParen guards the invariant the planner
// depends on: a parenthesised operand with nothing after its ')' needs no
// grouping node and must stay Parenthesized, so the flattening loop treats it
// as atomic. A grouping node here would be harmless but wasteful; losing the
// flag would silently re-flatten user-written grouping.
func TestNoGroupingNodeWhenNothingFollowsParen(t *testing.T) {
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
			if right.SetOpOperand != nil {
				t.Errorf("%s: got a grouping node, want none (nothing follows the ')')", sql)
			}
		})
	}
}

// TestParenthesizedFlagUnaffectedForInsertSource pins that the INSERT source
// disambiguation (`INSERT INTO t (SELECT …)`, which reuses Parenthesized to
// tell a query source from a column list) is untouched: no set-operator
// follows the ')', so no grouping node appears and the flag stays set.
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
	if ins.Select.SetOpOperand != nil {
		t.Errorf("got a grouping node, want none")
	}
}

// TestSortLimitLandsOnItsOwnNode is the heart of M0125-0020: an ORDER BY /
// LIMIT written INSIDE the parentheses stays on the parenthesised branch, and
// one written after the ')' lands on the grouping node above it. Under the
// linked list both competed for the head node's single slot, which is what
// InnerSegmentCount and InnerSortLimit tried to arbitrate — and why the last
// case below could not be represented at all.
func TestSortLimitLandsOnItsOwnNode(t *testing.T) {
	cases := []struct {
		why string
		sql string
		// innerPath locates the node that must own the sort/limit written
		// inside the parentheses (nil when there is none).
		innerPath []string
		wantOrder bool // inner node has ORDER BY
		wantLimit bool // inner node has LIMIT
		rootOrder bool // the root (whole-chain) node has ORDER BY
		rootLimit bool // the root has LIMIT
	}{
		{
			why:       "single parenthesised head branch keeps its own sort+limit",
			sql:       "(SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL SELECT x FROM h",
			innerPath: []string{"operand"},
			wantOrder: true, wantLimit: true,
		},
		{
			why:       "LIMIT alone is enough",
			sql:       "(SELECT x FROM a LIMIT 2) UNION ALL SELECT x FROM h",
			innerPath: []string{"operand"},
			wantLimit: true,
		},
		{
			why:       "redundant parentheses do not move the clauses",
			sql:       "((SELECT x FROM a ORDER BY 1 LIMIT 2)) UNION ALL SELECT x FROM h",
			innerPath: []string{"operand"},
			wantOrder: true, wantLimit: true,
		},
		{
			// M0097-0044's shape: the ORDER BY was lifted to the head of the
			// INTERSECT chain inside the parentheses, and the grouping node
			// keeps the UNION ALL out of its way.
			why:       "inner compound's ORDER BY stays on the inner chain head",
			sql:       "((SELECT x FROM a INTERSECT SELECT x FROM g ORDER BY 1)) UNION ALL SELECT x FROM h",
			innerPath: []string{"operand"},
			wantOrder: true,
		},
		{
			why:       "an outer paren level does not widen an inner branch's clauses",
			sql:       "((SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL SELECT x FROM h) EXCEPT SELECT x FROM g",
			innerPath: []string{"operand", "operand"},
			wantOrder: true, wantLimit: true,
		},
		{
			why:       "written after the ')': belongs to the whole chain",
			sql:       "(SELECT x FROM a) UNION ALL (SELECT x FROM h) ORDER BY 1 LIMIT 2",
			innerPath: []string{"operand"},
			rootOrder: true, rootLimit: true,
		},
		{
			// The shape the linked list could not represent: the inner LIMIT 2
			// used to be overwritten by the outer LIMIT 1. Both survive now,
			// on their own nodes.
			why:       "inner LIMIT and outer LIMIT coexist",
			sql:       "((SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL SELECT x FROM h) LIMIT 1",
			innerPath: []string{"operand", "operand"},
			wantOrder: true, wantLimit: true,
			rootLimit: true,
		},
	}
	for _, c := range cases {
		t.Run(c.why, func(t *testing.T) {
			root := parseOneSelect(t, c.sql)
			if got := root.OrderBy != nil; got != c.rootOrder {
				t.Errorf("%s: root ORDER BY=%v, want %v", c.sql, got, c.rootOrder)
			}
			if got := root.Limit != nil; got != c.rootLimit {
				t.Errorf("%s: root LIMIT=%v, want %v", c.sql, got, c.rootLimit)
			}
			inner := nodeAt(t, root, c.innerPath...)
			if got := inner.OrderBy != nil; got != c.wantOrder {
				t.Errorf("%s: inner ORDER BY=%v, want %v", c.sql, got, c.wantOrder)
			}
			if got := inner.Limit != nil; got != c.wantLimit {
				t.Errorf("%s: inner LIMIT=%v, want %v", c.sql, got, c.wantLimit)
			}
		})
	}
}
