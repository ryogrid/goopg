package optimizer

// M0148-0001j — ordered aggregates are never hashed, the elected aggregate
// marks PG's aggpresorted, and transition states are shared only between
// aggregates with equal ORDER BY clauses.

import "testing"

// TestOrderedSetAggregateGroupingIsNotHashed: `mode() WITHIN GROUP` has no
// presorted keys (the selection skips ordered-set aggregates), and PG's
// numOrderedAggs still forbids hashing, so the grouping sorts by the group
// key — goopg used to hash it and print the groups in hash order
// (aggregates.sql).
func TestOrderedSetAggregateGroupingIsNotHashed(t *testing.T) {
	cat := presortedAggCatalog(t)
	node, err := Plan(parseOne(t, "select ten, mode() within group (order by two) from tenk1 group by ten"), cat)
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	if a.Strategy != AggStrategySorted {
		t.Fatalf("Strategy = %d, want AggStrategySorted (no hashing with ordered aggregates)", a.Strategy)
	}
	assertSortKeys(t, a, []string{"ten"})
}

// TestDistinctAggregateGroupingSortsWithPresortOff: with
// enable_presorted_aggregate off there are no presorted keys, and the
// group-key Sort is the grouping's only candidate (the executor sorts each
// group's DISTINCT inputs); nothing is marked presorted.
func TestDistinctAggregateGroupingSortsWithPresortOff(t *testing.T) {
	cat := presortedAggCatalog(t)
	node, err := PlanWithSettings(parseOne(t, "select ten, sum(distinct two) from tenk1 group by ten"), cat, presortedSettings(false))
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	if a.Strategy != AggStrategySorted {
		t.Fatalf("Strategy = %d, want AggStrategySorted", a.Strategy)
	}
	assertSortKeys(t, a, []string{"ten"})
	if a.Aggs[0].Presorted {
		t.Fatal("aggregate marked presorted although its input is sorted by the group key only")
	}
}

// TestPresortedAggregatesAreMarked: the covered aggregate is marked, a plain
// one is not, and of two aggregates with incomparable sort lists only the
// one whose keys the Sort carries is (bestaggs).
func TestPresortedAggregatesAreMarked(t *testing.T) {
	cat := presortedAggCatalog(t)
	node, err := Plan(parseOne(t, "select ten, sum(distinct two), sum(unique1) from tenk1 group by ten"), cat)
	if err != nil {
		t.Fatal(err)
	}
	a := presortedAggPlan(t, node)
	assertSortKeys(t, a, []string{"ten", "two"})
	if !a.Aggs[0].Presorted || a.Aggs[1].Presorted {
		t.Fatalf("Presorted = %v/%v, want true/false", a.Aggs[0].Presorted, a.Aggs[1].Presorted)
	}

	node, err = Plan(parseOne(t, "select ten, sum(unique1 order by two), sum(unique1 order by four) from tenk1 group by ten"), cat)
	if err != nil {
		t.Fatal(err)
	}
	a = presortedAggPlan(t, node)
	assertSortKeys(t, a, []string{"ten", "two"})
	if !a.Aggs[0].Presorted || a.Aggs[1].Presorted {
		t.Fatalf("Presorted = %v/%v, want true/false (only bestaggs is presorted)", a.Aggs[0].Presorted, a.Aggs[1].Presorted)
	}
}

// TestSharedStateSlotSeparatesOrderBy: find_compatible_trans shares a state
// only between aggregates with equal ORDER BY clauses; `ua_sum(a ORDER BY a
// DESC)` and `ua_sum(a)` (or `ua_sum(a ORDER BY a)`) each run their own
// transition calls.
func TestSharedStateSlotSeparatesOrderBy(t *testing.T) {
	cat := userAggCatalog(t)
	agg := aggregateNodeOf(t, cat, "SELECT ua_sum(a ORDER BY a DESC), ua_sum(a) FROM agg_t")
	if len(agg.Aggs) != 2 {
		t.Fatalf("expected 2 aggregate calls, got %d", len(agg.Aggs))
	}
	if agg.Aggs[0].SharedStateSlot == agg.Aggs[1].SharedStateSlot {
		t.Errorf("ORDER BY and plain calls share slot %d", agg.Aggs[0].SharedStateSlot)
	}
	// The direction is part of the clause.
	agg = aggregateNodeOf(t, cat, "SELECT ua_sum(a ORDER BY a DESC), ua_sum(a ORDER BY a) FROM agg_t")
	if len(agg.Aggs) == 2 && agg.Aggs[0].SharedStateSlot == agg.Aggs[1].SharedStateSlot {
		t.Errorf("DESC and ASC ORDER BY calls share slot %d", agg.Aggs[0].SharedStateSlot)
	}
}
