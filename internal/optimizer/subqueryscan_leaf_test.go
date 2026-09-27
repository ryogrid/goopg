package optimizer

// M0146-0005w: the SubqueryScan leaf wrapper.
//
// PG's planner keeps a FROM-clause subquery it cannot pull up
// (is_simple_subquery, prepjointree.c) as an RTE_SUBQUERY and renders it
// as `Subquery Scan on <alias>` — a single scan leaf in the plan-census
// leaf set. goopg used to hand the inner subtree to the jointree
// directly, so a set-operation arm read as its innermost relations
// (TPC-DS Q8's `customer`/`customer_address` pair) instead of the one
// leaf PG reports.
//
// These tests pin the planner-side contract: the wrapper appears exactly
// on non-simple derived tables, carries the binding's alias and
// (possibly renamed) output schema, and stays invisible on shapes PG
// pulls up. Execution transparency and EXPLAIN rendering are pinned in
// internal/executor/subqueryscan_explain_test.go.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func subqueryScanFixture(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	create := func(name string, cols []catalog.Column) {
		t.Helper()
		if _, err := cat.CreateTable(parser.ObjectName{Name: name}, cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
	}
	create("t1", []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
	})
	create("t2", []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	})
	return cat
}

// findSubqueryScan returns the first *SubqueryScan in the plan (DFS),
// using the same reflection-driven child discovery the optimizer's own
// generic walkers use, so the hunt itself does not under-count when a
// new node kind sits on the path.
func findSubqueryScan(n Node) *SubqueryScan {
	if n == nil {
		return nil
	}
	if sq, ok := n.(*SubqueryScan); ok {
		return sq
	}
	kids, ok := planChildNodes(n)
	if !ok {
		return nil
	}
	for _, k := range kids {
		if sq := findSubqueryScan(k); sq != nil {
			return sq
		}
	}
	return nil
}

// subtreeHolds reports whether the subtree rooted at n contains a node
// of the wanted kind; used to confirm the wrapper kept its child plan.
func subtreeHolds(n Node, want func(Node) bool) bool {
	if n == nil {
		return false
	}
	if want(n) {
		return true
	}
	kids, ok := planChildNodes(n)
	if !ok {
		return false
	}
	for _, k := range kids {
		if subtreeHolds(k, want) {
			return true
		}
	}
	return false
}

func planSQL(t *testing.T, cat catalog.Catalog, sql string) Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

func TestSubqueryScanWrapsSetOperationArm(t *testing.T) {
	cat := subqueryScanFixture(t)
	// Non-trivial consumption keeps the label: u publishes two columns
	// and the outer scope reads only the first — the leaf is at top
	// level, so upstream's pathtarget regime applies (CP_EXACT_TLIST)
	// and trivial_subqueryscan declines on the subset tlist. Under a
	// join PG would strip it regardless — the physical tlist regime —
	// see the M0146-0005x cases in TestSubqueryScanStripsTrivialWrapper.
	plan := planSQL(t, cat,
		"select u.a from (select a, b from t1 intersect select a, b from t1) u")
	sq := findSubqueryScan(plan)
	if sq == nil {
		t.Fatalf("no *SubqueryScan in plan for a set-operation derived table")
	}
	if sq.Alias != "u" {
		t.Errorf("SubqueryScan.Alias = %q, want %q", sq.Alias, "u")
	}
	if sq.Child == nil {
		t.Fatal("SubqueryScan.Child is nil — the derived table's plan was dropped")
	}
	if !subtreeHolds(sq.Child, func(n Node) bool { _, ok := n.(*SetOp); return ok }) {
		t.Errorf("SubqueryScan child subtree lost the SetOp — the wrapper must preserve the child plan")
	}
	if len(sq.Output()) != len(sq.Child.Output()) {
		t.Errorf("SubqueryScan output width %d != child width %d — the label is a position-for-position mapping",
			len(sq.Output()), len(sq.Child.Output()))
	}
}

func TestSubqueryScanWrapsGroupedDerivedTable(t *testing.T) {
	cat := subqueryScanFixture(t)
	// `u.q` alone is a subset of the published [p q] at top level —
	// pathtarget regime, non-trivial, so the wrapper survives the
	// triviality strip.
	plan := planSQL(t, cat,
		"select u.q from (select a, count(a) c from t1 group by a) u(p, q)")
	sq := findSubqueryScan(plan)
	if sq == nil {
		t.Fatalf("no *SubqueryScan in plan for a grouped derived table")
	}
	if !subtreeHolds(sq.Child, func(n Node) bool { _, ok := n.(*Aggregate); return ok }) {
		t.Errorf("SubqueryScan child subtree lost the Aggregate")
	}
	// The schema is the binding's — the column rename must be what the
	// outer scope resolves, not the subquery's own target names.
	out := sq.Output()
	if len(out) < 2 || out[0].Name != "p" || out[1].Name != "q" {
		t.Errorf("SubqueryScan schema = %v, want alias-renamed [p q]", out)
	}
}

func TestSubqueryScanSkipsSimpleSubquery(t *testing.T) {
	cat := subqueryScanFixture(t)
	plan := planSQL(t, cat,
		"select u.a from t1, (select a from t1 where a > 0) u where t1.a = u.a")
	if sq := findSubqueryScan(plan); sq != nil {
		t.Errorf("simple derived table got a SubqueryScan (alias %q) — PG pulls this shape up, "+
			"so the label must not appear", sq.Alias)
	}
}

func TestSubqueryScanNestedDerivedTables(t *testing.T) {
	cat := subqueryScanFixture(t)
	// Q8's actual shape: a derived table whose own FROM holds a set-op
	// derived table. The outer level is simple, so only the inner one
	// can label — and it does: v's own select list reads only `b`, a
	// subset consumption of x's [a b], non-trivial under
	// trivial_subqueryscan (had v selected `a, b` the strip would fire —
	// that is the pinned case below).
	plan := planSQL(t, cat,
		"select v.b from (select b from (select a, b from t1 intersect select a, b from t1) x) v")
	found := 0
	var count func(n Node)
	count = func(n Node) {
		if n == nil {
			return
		}
		if _, ok := n.(*SubqueryScan); ok {
			found++
		}
		if kids, ok := planChildNodes(n); ok {
			for _, k := range kids {
				count(k)
			}
		}
	}
	count(plan)
	if found == 0 {
		t.Error("no SubqueryScan in a nested set-op derived table — the set-op arm must be wrapped")
	}
}

// TestSubqueryScanStripsTrivialWrapper pins the setrefs.c half of the
// ticket: a SubqueryScan whose enclosing scope consumes its full output
// in order, with no leaf-level qual, is removed — the live-PG probes
// showed the label vanishing on `SELECT u.a, u.c` over a two-column
// grouped derived table and reappearing on `SELECT u.c, u.a` or a
// subset. Emission is non-simple-only; stripping is consumption-only.
func TestSubqueryScanStripsTrivialWrapper(t *testing.T) {
	cat := subqueryScanFixture(t)
	for _, tc := range []struct {
		name    string
		sql     string
		wantNil bool
	}{
		{"full in-order grouped leaf strips",
			"select u.a, u.c from (select a, count(a) c from t1 group by a) u", true},
		{"full in-order set-op leaf strips",
			"select u.a from t1, (select a from t1 intersect select a from t2) u where t1.a = u.a", true},
		{"out-of-order consumption keeps",
			"select u.c, u.a from (select a, count(a) c from t1 group by a) u", false},
		{"subset consumption keeps",
			"select u.a from (select a, count(a) c from t1 group by a) u", false},
		// M0146-0005x — the physical-tlist regime: a leaf below a
		// spine-breaker (join, aggregate) gets the subquery's full
		// output tlist in attno order, so trivial_subqueryscan strips
		// it no matter which subset the outer scope reads. Verified on
		// PG 18.3 (`select u.a … join` → bare Merge Join input;
		// `select count(*) …` → bare Aggregate input).
		{"subset consumption under join strips",
			"select u.a, t1.b from t1, (select a, count(a) c from t1 group by a) u where t1.a = u.a", true},
		{"subset consumption under top aggregate strips",
			"select count(*) from (select a, count(a) c from t1 group by a) u", true},
		// Spine-continuing ancestors keep the pathtarget regime: the
		// leaf's tlist is the needed columns in first-needed order, so
		// a subset still declines (PG verified under Sort, Limit and
		// Unique alike).
		{"subset under sort keeps",
			"select u.a from (select a, count(a) c from t1 group by a) u order by u.a", false},
		{"subset under limit keeps",
			"select u.a from (select a, count(a) c from t1 group by a) u limit 5", false},
		{"subset under distinct keeps",
			"select distinct u.a from (select a, count(a) c from t1 group by a) u", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sq := findSubqueryScan(planSQL(t, cat, tc.sql))
			if tc.wantNil && sq != nil {
				t.Errorf("trivial wrapper kept (alias %q) — PG's setrefs strips it", sq.Alias)
			}
			if !tc.wantNil && sq == nil {
				t.Error("non-trivial wrapper stripped — PG keeps the label on subset/reordered consumption")
			}
		})
	}
}
