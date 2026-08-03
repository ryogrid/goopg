package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// selfJoinGroupByCatalog builds the two-table shape that reproduces M0125-0044:
// a dimension table joined to a fact table twice under different aliases, which
// is TPC-DS Q64's `date_dim d1/d2/d3` reduced to its essentials.
func selfJoinGroupByCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "dim"}, []catalog.Column{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
		{Name: "y", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "fact"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// projectTargets returns the plan's projection targets. None of the queries
// below carry ORDER BY or LIMIT, so the root is the Project itself.
func projectTargets(t *testing.T, n Node) []Expr {
	t.Helper()
	p, ok := n.(*Project)
	if !ok {
		t.Fatalf("plan root is %T, want *Project", n)
	}
	return p.Targets
}

// groupKeySlots returns the group-key slot each of the first n targets reads,
// or -1 for a target that is not a bare output ColumnRef.
func groupKeySlots(targets []Expr, n int) []int {
	out := make([]int, 0, n)
	for i := 0; i < n && i < len(targets); i++ {
		if cr, ok := targets[i].(*ColumnRef); ok {
			out = append(out, cr.Index)
			continue
		}
		out = append(out, -1)
	}
	return out
}

// TestGroupByAliasedSelfJoinProjectsDistinctSlots is the M0125-0044 regression.
//
// parserExprKey is qualifier-blind on purpose — GROUP BY c must satisfy SELECT
// t.c (M0097-0003) — so `d1.y` and `d2.y` both hash to "c:y". The aggregate
// surface keyed its GROUP-BY-to-output map on that string, so the second item
// overwrote the first and BOTH target columns resolved to the last slot. The
// grouping itself was correct (GroupExprs holds two distinct resolved refs), so
// the row count and group count matched PostgreSQL exactly while the projected
// values were wrong: TPC-DS Q64 reported first-sales years as sold years and
// its final `syear = 1999` filter then emptied a 2-row answer to 0.
func TestGroupByAliasedSelfJoinProjectsDistinctSlots(t *testing.T) {
	cat := selfJoinGroupByCatalog(t)
	stmt := parseOne(t, `SELECT d1.y, d2.y, count(*) FROM fact, dim d1, dim d2
	                     WHERE fact.a = d1.k AND fact.b = d2.k GROUP BY 1, 2`)
	plan, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := groupKeySlots(projectTargets(t, plan), 2)
	if got[0] == got[1] {
		t.Fatalf("d1.y and d2.y both project group-key slot %d; the two aliases collapsed", got[0])
	}
	if got[0] != 0 || got[1] != 1 {
		t.Fatalf("target slots = %v, want [0 1] (GROUP BY order)", got)
	}
}

// TestGroupByAliasedSelfJoinExplicitExprsProjectDistinctSlots pins the same
// property for spelled-out GROUP BY items. `GROUP BY 1, 2` is rewritten to the
// target expressions by resolveOrderBySubstitution before the key is taken, so
// the two spellings share a code path — but only one of them was measured
// against PostgreSQL, and a positional-only test would not have caught a
// regression in the substitution.
func TestGroupByAliasedSelfJoinExplicitExprsProjectDistinctSlots(t *testing.T) {
	cat := selfJoinGroupByCatalog(t)
	stmt := parseOne(t, `SELECT d1.y, d2.y, count(*) FROM fact, dim d1, dim d2
	                     WHERE fact.a = d1.k AND fact.b = d2.k GROUP BY d1.y, d2.y`)
	plan, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := groupKeySlots(projectTargets(t, plan), 2)
	if got[0] != 0 || got[1] != 1 {
		t.Fatalf("target slots = %v, want [0 1]", got)
	}
}

// TestGroupByAliasedSelfJoinComputedKeysProjectDistinctSlots covers the same
// collapse one level up, where the contested key belongs to a computed
// expression rather than a bare column: `d1.y + 0` and `d2.y + 0` both key as
// "b:+:(c:y):(i:0)". A bare column can be placed from the input-column map, but
// a computed key has no entry there and has to be matched structurally against
// the resolved GROUP BY expressions.
func TestGroupByAliasedSelfJoinComputedKeysProjectDistinctSlots(t *testing.T) {
	cat := selfJoinGroupByCatalog(t)
	stmt := parseOne(t, `SELECT d1.y + 0, d2.y + 0, count(*) FROM fact, dim d1, dim d2
	                     WHERE fact.a = d1.k AND fact.b = d2.k GROUP BY 1, 2`)
	plan, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := groupKeySlots(projectTargets(t, plan), 2)
	if got[0] == got[1] {
		t.Fatalf("d1.y+0 and d2.y+0 both project group-key slot %d", got[0])
	}
	if got[0] != 0 || got[1] != 1 {
		t.Fatalf("target slots = %v, want [0 1]", got)
	}
}

// TestGroupByRepeatedColumnIsNotAmbiguous guards the other direction: GROUP BY
// a, a names ONE slot twice, which is not a contested key and must not be
// treated as one. The ambiguity check therefore asks "is this key already bound
// to a different slot", not "is this key a duplicate".
func TestGroupByRepeatedColumnIsNotAmbiguous(t *testing.T) {
	cat := selfJoinGroupByCatalog(t)
	stmt := parseOne(t, `SELECT d1.y, count(*) FROM fact, dim d1
	                     WHERE fact.a = d1.k GROUP BY d1.y, d1.y`)
	plan, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := groupKeySlots(projectTargets(t, plan), 1)
	if got[0] < 0 || got[0] > 1 {
		t.Fatalf("target slot = %v, want a group-key slot (0 or 1)", got)
	}
}

// TestGroupByUnqualifiedKeySatisfiesQualifiedRef is the property parserExprKey's
// qualifier-blindness exists to provide (M0097-0003), and the one this fix must
// not cost: with a single binding in scope, GROUP BY y satisfies SELECT d1.y.
func TestGroupByUnqualifiedKeySatisfiesQualifiedRef(t *testing.T) {
	cat := selfJoinGroupByCatalog(t)
	stmt := parseOne(t, `SELECT d1.y, count(*) FROM fact, dim d1
	                     WHERE fact.a = d1.k GROUP BY y`)
	if _, err := Plan(stmt, cat); err != nil {
		t.Fatalf("GROUP BY y should satisfy SELECT d1.y: %v", err)
	}
}

// TestSelectRefNotInGroupByStillRejected confirms the fix did not turn a
// 42803 into a silently wrong slot: a third alias that is not grouped at all
// must still be rejected, even though its name collides with the contested key.
func TestSelectRefNotInGroupByStillRejected(t *testing.T) {
	cat := selfJoinGroupByCatalog(t)
	stmt := parseOne(t, `SELECT d1.y, d2.y, d3.y, count(*) FROM fact, dim d1, dim d2, dim d3
	                     WHERE fact.a = d1.k AND fact.b = d2.k GROUP BY d1.y, d2.y`)
	_, err := Plan(stmt, cat)
	if err == nil {
		t.Fatalf("d3.y is in neither GROUP BY nor an aggregate; expected 42803")
	}
	pe, ok := err.(*PlanError)
	if !ok || pe.Code != "42803" {
		t.Fatalf("want PlanError 42803, got %#v", err)
	}
}
