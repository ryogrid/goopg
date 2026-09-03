package optimizer

import (
	"testing"
)

// S8 Slice 2b (0134-0001 P2) unit tests for the enable_hashagg bridge — the
// port of PG's cost-model outcome when `SET enable_hashagg = off`
// (postgres/src/backend/optimizer/path/costsize.c:2755-2756 cost_agg disables
// the AGG_HASHED arm, so the sorted path wins). Each test plans an aggregates
// probe against the shared tenk1-shaped catalog (see presortedAggCatalog) and
// inspects the Aggregate node's Sort child / Strategy.

// TestEnableHashAggOffForcesSorted: with the GUC off, a plain grouped
// aggregate must plan as GroupAggregate (Strategy Sorted) over an ascending
// Sort carrying exactly one key per GroupExpr, each with Desc/NullsFirst
// false — the aggregates.out:3457 shape (`set enable_hashagg = false` →
// GroupAggregate → Sort → Seq Scan).
func TestEnableHashAggOffForcesSorted(t *testing.T) {
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, "select sum(unique1) from tenk1 group by ten")
	node, err := PlanWithSettings(stmt, cat, hashAggSettings(false))
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	if a.Strategy != AggStrategySorted {
		t.Fatalf("Strategy = %d, want AggStrategySorted", a.Strategy)
	}
	s, ok := a.Child.(*Sort)
	if !ok {
		t.Fatalf("Aggregate.Child is %T, want *Sort", a.Child)
	}
	if len(s.Keys) != len(a.GroupExprs) {
		t.Fatalf("Sort has %d keys, want %d (one per GroupExpr)", len(s.Keys), len(a.GroupExprs))
	}
	for i, k := range s.Keys {
		if k.Desc {
			t.Fatalf("Sort.Keys[%d].Desc = true, want false (ascending)", i)
		}
		if k.NullsFirst {
			t.Fatalf("Sort.Keys[%d].NullsFirst = true, want false", i)
		}
	}
	assertSortKeys(t, a, []string{"ten"})
}

// TestEnableHashAggDefaultInert: with the GUC on (the default), the rule must
// leave the plan untouched — Strategy stays Hashed, no Sort child. Default-on
// is inert: no existing query changes.
func TestEnableHashAggDefaultInert(t *testing.T) {
	SetHashAggEnabled(true)
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, "select sum(unique1) from tenk1 group by ten")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	if a.Strategy != AggStrategyHashed {
		t.Fatalf("Strategy = %d, want AggStrategyHashed with GUC on (default)", a.Strategy)
	}
	if _, ok := a.Child.(*Sort); ok {
		t.Fatalf("rule fired with GUC on: Aggregate.Child is *Sort")
	}
}

// TestEnableHashAggSkipsAlreadySorted: a grouped query with an internal ORDER
// BY is claimed by the presorted rule first (Strategy Sorted); the
// enable_hashagg rule must NOT double-wrap it. The Sort keys stay the
// presorted set [ten, two] — a wrongful re-wrap by the hashagg rule would
// replace them with the bare group-key set [ten].
func TestEnableHashAggSkipsAlreadySorted(t *testing.T) {
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, "select sum(unique1 order by two) from tenk1 group by ten")
	node, err := PlanWithSettings(stmt, cat, hashAggSettings(false))
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	if a.Strategy != AggStrategySorted {
		t.Fatalf("Strategy = %d, want AggStrategySorted (presorted rule claimed it)", a.Strategy)
	}
	assertSortKeys(t, a, []string{"ten", "two"})
}

// TestEnableHashAggSkipsGroupingSets: GROUPING SETS always hash in goopg's
// executor (one hash table per set) and PG's cost_agg has no SORTED arm for
// them, so the rule must leave the node Hashed with no Sort child even with
// the GUC off.
func TestEnableHashAggSkipsGroupingSets(t *testing.T) {
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, "select sum(unique1) from tenk1 group by grouping sets ((ten), (two))")
	node, err := PlanWithSettings(stmt, cat, hashAggSettings(false))
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	if a.Strategy != AggStrategyHashed {
		t.Fatalf("Strategy = %d, want AggStrategyHashed (grouping sets always hash)", a.Strategy)
	}
	if _, ok := a.Child.(*Sort); ok {
		t.Fatalf("rule fired on grouping sets: Aggregate.Child is *Sort")
	}
}

// TestEnableHashAggSkipsUngrouped: with no GROUP BY (len(GroupExprs)==0) the
// aggregate is non-grouped and must keep its plain label — no Sort child, no
// Strategy flip.
func TestEnableHashAggSkipsUngrouped(t *testing.T) {
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, "select sum(unique1) from tenk1")
	node, err := PlanWithSettings(stmt, cat, hashAggSettings(false))
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	if a.Strategy != AggStrategyHashed {
		t.Fatalf("Strategy = %d, want AggStrategyHashed (ungrouped must keep plain label)", a.Strategy)
	}
	if _, ok := a.Child.(*Sort); ok {
		t.Fatalf("rule fired on ungrouped aggregate: Aggregate.Child is *Sort")
	}
}

// TestEnableHashAggSkipsNonSimpleMode: a Mode != AggModeSimple node (parallel
// Partial/Final) is not shaped by the GUC. Not reachable via a single-statement
// Plan() in this harness, so construct the Aggregate node directly: the rule
// must leave both the Strategy and the child untouched.
func TestEnableHashAggSkipsNonSimpleMode(t *testing.T) {
	aggNode := &Aggregate{
		GroupExprs: []Expr{&ColumnRef{Name: "ten"}},
		Mode:       AggModePartial,
		Strategy:   AggStrategyHashed,
	}
	applyEnableHashAggRule(aggNode, hashAggSettings(false))
	if aggNode.Strategy != AggStrategyHashed {
		t.Fatalf("Strategy = %d, want AggStrategyHashed (Mode != AggModeSimple must skip)", aggNode.Strategy)
	}
	if aggNode.Child != nil {
		t.Fatalf("rule wrapped the child on a Mode != AggModeSimple node: got %T", aggNode.Child)
	}
}

// hashAggSettings is the take2 P2-02c replacement for SetHashAggEnabled in
// these tests: enable_hashagg is now a per-statement planner input, so a test
// states the setting it wants rather than mutating a process global.
func hashAggSettings(on bool) PlannerSettings {
	ps := DefaultPlannerSettings()
	ps.EnableHashAgg = on
	return ps
}
