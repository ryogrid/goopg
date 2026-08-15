package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// S8 Slice 2a (0134-0001 P2) unit tests for the presorted-aggregate rule —
// the port of adjust_group_pathkeys_for_groupagg (planner.c:3229). Each test
// plans one of the aggregates.sql EXPLAIN probes against a tenk1-shaped table
// and inspects the Aggregate node's Sort child / Strategy.

// presortedAggCatalog seeds a tenk1-shaped table with the columns the
// aggregates.sql presort probes reference (unique1, ten, two, four).
func presortedAggCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "tenk1"}, []catalog.Column{
		{Name: "unique1", Type: catalog.Type{Name: "int4"}},
		{Name: "ten", Type: catalog.Type{Name: "int4"}},
		{Name: "two", Type: catalog.Type{Name: "int4"}},
		{Name: "four", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// presortedAggPlan digs the Aggregate node out of a plan rooted at Project.
func presortedAggPlan(t *testing.T, node Node) *Aggregate {
	t.Helper()
	p, ok := node.(*Project)
	if !ok {
		t.Fatalf("plan root is %T, want *Project", node)
	}
	for c := p.Child; c != nil; {
		if a, ok := c.(*Aggregate); ok {
			return a
		}
		switch x := c.(type) {
		case *Filter:
			c = x.Child
		case *Project:
			c = x.Child
		default:
			t.Fatalf("no Aggregate under Project (stopped at %T)", c)
		}
	}
	t.Fatalf("no Aggregate under Project")
	return nil
}

// sortKeyNames reads the Aggregate's Sort child's key names; nil when there is
// no Sort child (the rule must have left the plan untouched).
func sortKeyNames(t *testing.T, a *Aggregate) []string {
	t.Helper()
	if a.Child == nil {
		return nil
	}
	s, ok := a.Child.(*Sort)
	if !ok {
		t.Fatalf("Aggregate.Child is %T, want *Sort", a.Child)
	}
	names := make([]string, len(s.Keys))
	for i, k := range s.Keys {
		cr, ok := k.Expr.(*ColumnRef)
		if !ok {
			t.Fatalf("Sort.Keys[%d].Expr is %T, want *ColumnRef", i, k.Expr)
		}
		names[i] = cr.Name
	}
	return names
}

func assertSortKeys(t *testing.T, a *Aggregate, want []string) {
	t.Helper()
	names := sortKeyNames(t, a)
	if len(names) != len(want) {
		t.Fatalf("Sort keys = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("Sort keys = %v, want %v", names, want)
		}
	}
}

// TestPresortedAggregateGreedyGrouped: the aggregates.sql grouped probe with a
// GROUP BY column inside one aggregate's ORDER BY — the greedy pick must be the
// set `[ten, two, four]` (group key ten prefixed, then the two-key agg that
// dominates) and a grouped query must flip to AggStrategySorted.
func TestPresortedAggregateGreedyGrouped(t *testing.T) {
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, `select
		sum(unique1 order by ten, two), sum(unique1 order by four),
		sum(unique1 order by two, four)
		from tenk1
		group by ten`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	assertSortKeys(t, a, []string{"ten", "two", "four"})
	if a.Strategy != AggStrategySorted {
		t.Fatalf("Strategy = %d, want AggStrategySorted", a.Strategy)
	}
}

// TestPresortedAggregateVolatileExcluded: the aggregates.sql probe where two
// aggregates carry volatile ORDER BY functions (random()) — those must be
// dropped from consideration, leaving the three clean aggregates to resolve to
// `[ten, four, two]` (the strongest clean set, then the extra two key).
func TestPresortedAggregateVolatileExcluded(t *testing.T) {
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, `select
		sum(unique1 order by two), sum(unique1 order by four),
		sum(unique1 order by four, two), sum(unique1 order by two, random()),
		sum(unique1 order by two, random(), random() + 1)
		from tenk1
		group by ten`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	assertSortKeys(t, a, []string{"ten", "four", "two"})
	if a.Strategy != AggStrategySorted {
		t.Fatalf("Strategy = %d, want AggStrategySorted", a.Strategy)
	}
}

// TestPresortedAggregateNonGroupedTiebreak: the non-grouped probe where two and
// four each cover two aggregates — the tie must break on target-list position
// (`two` wins) — and the non-grouped Aggregate must keep its plain label
// (Strategy must NOT be flipped).
func TestPresortedAggregateNonGroupedTiebreak(t *testing.T) {
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, `select
		sum(two order by two), max(four order by four),
		min(four order by four), max(two order by two)
		from tenk1`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	assertSortKeys(t, a, []string{"two"})
	if a.Strategy != AggStrategyHashed {
		t.Fatalf("Strategy = %d, want AggStrategyHashed (non-grouped must keep plain label)", a.Strategy)
	}
}

// TestPresortedAggregateGucOff: with the GUC kill-switch off, the rule must
// leave the plan untouched — no Sort child, no Strategy flip.
func TestPresortedAggregateGucOff(t *testing.T) {
	SetPresortedAggEnabled(false)
	defer SetPresortedAggEnabled(true)
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, "select sum(two order by two) from tenk1")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	if _, ok := a.Child.(*Sort); ok {
		t.Fatalf("rule fired with GUC off: Aggregate.Child is *Sort")
	}
	if a.Strategy != AggStrategyHashed {
		t.Fatalf("Strategy = %d, want AggStrategyHashed with GUC off", a.Strategy)
	}
}
