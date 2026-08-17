package optimizer

// setop_head_sortlimit_test.go — M0125-0017, planner half.
//
// The executor half (internal/executor/setop_head_branch_sortlimit_test.go)
// pins the ANSWERS against PostgreSQL 18.3. This file pins the two structural
// invariants those answers depend on, which a value assertion cannot see:
//
//  1. Where the Limit lands. `(A ORDER BY 1 LIMIT 2) UNION ALL C` must build
//     SetOp(Limit(Sort(A)), C) — the limit BELOW the set operation. The old
//     shape, Limit(Sort(SetOp(A, C))), also returns a plausible row count.
//  2. That planning does not consume the AST. planSelect used to blank
//     s.OrderBy/Limit/Offset to stop the outer wrapper re-applying an inner
//     boundary's sort, so a second Plan() of the SAME SelectStmt — which the
//     plan cache can ask for — silently produced an unlimited plan.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// findLimitUnderSetOpLeft walks a plan looking for the first SetOp and reports
// whether its LEFT input contains a Limit, and whether a Limit sits above the
// SetOp. Project/Sort/Filter wrappers are transparent.
func findLimitUnderSetOpLeft(n Node) (belowLeft, above bool) {
	var sawLimit bool
	var walk func(Node) bool // returns true once a SetOp was reached
	hasLimit := func(n Node) bool {
		var rec func(Node) bool
		rec = func(n Node) bool {
			switch t := n.(type) {
			case nil:
				return false
			case *Limit:
				return true
			case *Sort:
				return rec(t.Child)
			case *Project:
				return rec(t.Child)
			case *Filter:
				return rec(t.Child)
			case *SetOp:
				return rec(t.Left) || rec(t.Right)
			}
			return false
		}
		return rec(n)
	}
	walk = func(n Node) bool {
		switch t := n.(type) {
		case nil:
			return false
		case *SetOp:
			belowLeft = hasLimit(t.Left)
			above = sawLimit
			return true
		case *Limit:
			sawLimit = true
			return walk(t.Child)
		case *Sort:
			return walk(t.Child)
		case *Project:
			return walk(t.Child)
		case *Filter:
			return walk(t.Child)
		}
		return false
	}
	walk(n)
	return belowLeft, above
}

// TestPlanHeadBranchLimitSitsBelowSetOp pins invariant 1.
func TestPlanHeadBranchLimitSitsBelowSetOp(t *testing.T) {
	cat := pgbenchCatalog(t)
	const sql = "(SELECT aid FROM pgbench_accounts ORDER BY 1 LIMIT 2) " +
		"UNION ALL SELECT bid FROM pgbench_accounts"
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sel := stmts[0].(*parser.SelectStmt)
	// M0125-0020: the parenthesised head branch has its own SelectStmt below a
	// grouping node, and owns the ORDER BY / LIMIT written inside the parens.
	if sel.SetOpOperand == nil {
		t.Fatalf("parser: head branch is not below a grouping node")
	}
	if sel.SetOpOperand.Limit == nil || sel.Limit != nil {
		t.Fatalf("parser: LIMIT on operand=%v, on grouping node=%v; want true/false",
			sel.SetOpOperand.Limit != nil, sel.Limit != nil)
	}
	node, err := Plan(sel, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	belowLeft, above := findLimitUnderSetOpLeft(node)
	if !belowLeft {
		t.Errorf("%s: no Limit under the SetOp's left input — the head branch's "+
			"LIMIT was hoisted to the union: %T", sql, node)
	}
	if above {
		t.Errorf("%s: a Limit sits ABOVE the SetOp; the head branch's LIMIT must "+
			"not also apply to the union", sql)
	}
}

// TestPlanSetOpBoundaryIsReplannable pins invariant 2 for BOTH boundary kinds:
// the single-branch head boundary (M0125-0017) and the inner-compound boundary
// (M0097-0044). Planning must leave the SelectStmt exactly as parsed.
func TestPlanSetOpBoundaryIsReplannable(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{
			name: "head_branch_boundary",
			sql: "(SELECT aid FROM pgbench_accounts ORDER BY 1 LIMIT 2) " +
				"UNION ALL SELECT bid FROM pgbench_accounts",
		},
		{
			name: "inner_compound_boundary",
			sql: "((SELECT aid FROM pgbench_accounts INTERSECT " +
				"SELECT bid FROM pgbench_accounts ORDER BY 1 LIMIT 2)) " +
				"UNION ALL SELECT bid FROM pgbench_accounts",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cat := pgbenchCatalog(t)
			stmts, err := parser.Parse(c.sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			sel := stmts[0].(*parser.SelectStmt)
			// Both shapes put the clauses on the parenthesised branch's own
			// node, one level below the grouping node. M0125-0020.
			owner := sel
			if sel.SetOpOperand != nil {
				owner = sel.SetOpOperand
			}
			wantOrderBy, wantLimit, wantOffset := owner.OrderBy, owner.Limit, owner.Offset
			if wantLimit == nil {
				t.Fatalf("fixture: expected a LIMIT on the parsed statement")
			}
			first, err := Plan(sel, cat)
			if err != nil {
				t.Fatalf("Plan #1: %v", err)
			}
			if owner.OrderBy == nil || owner.Limit == nil {
				t.Fatalf("Plan #1 consumed the AST: OrderBy=%v Limit=%v",
					owner.OrderBy, owner.Limit)
			}
			if &owner.OrderBy[0] != &wantOrderBy[0] || owner.Limit != wantLimit || owner.Offset != wantOffset {
				t.Errorf("Plan #1 replaced the AST's sort/limit clauses")
			}
			second, err := Plan(sel, cat)
			if err != nil {
				t.Fatalf("Plan #2: %v", err)
			}
			b1, a1 := findLimitUnderSetOpLeft(first)
			b2, a2 := findLimitUnderSetOpLeft(second)
			if b1 != b2 || a1 != a2 {
				t.Errorf("replan differs: #1 (belowLeft=%v above=%v) #2 (belowLeft=%v above=%v)",
					b1, a1, b2, a2)
			}
		})
	}
}
