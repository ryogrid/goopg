package optimizer

import (
	"testing"
)

// These are the M0125-0045 regressions — the aggregate half of the M0125-0044
// collapse. aggregateCallKey builds its argument part on parserExprKey, which
// drops the table qualifier, so `count(d1.y)` and `count(d2.y)` over a
// self-joined table hash to one key and buildAggregateStage's exists-check
// discarded the second call: both SELECT columns then read agg slot 0 —
// right cardinality, wrong values, invisible to a row-count gate. PostgreSQL
// keys aggregate equality on the resolved argument Vars (equal() over
// Aggref->args, src/backend/nodes/equalfuncs.c), which separates the aliases
// by varno. The catalog/test shape is shared with the -0044 file
// (groupby_alias_collapse_test.go).

// aliasCollapseAggNode digs the Aggregate node out of a plan rooted at Project.
func aliasCollapseAggNode(t *testing.T, n Node) *Aggregate {
	t.Helper()
	p, ok := n.(*Project)
	if !ok {
		t.Fatalf("plan root is %T, want *Project", n)
	}
	for c := p.Child; c != nil; {
		if a, ok := c.(*Aggregate); ok {
			return a
		}
		switch x := c.(type) {
		case *Project:
			c = x.Child
		case *Filter:
			c = x.Child
		default:
			t.Fatalf("no Aggregate under Project (stopped at %T)", c)
		}
	}
	t.Fatalf("no Aggregate under Project")
	return nil
}

// TestAggregateAliasedSelfJoinDistinctSlots: count(d1.y) and count(d2.y) must
// occupy two aggregate slots and the two targets must each read their own.
func TestAggregateAliasedSelfJoinDistinctSlots(t *testing.T) {
	cat := selfJoinGroupByCatalog(t)
	stmt := parseOne(t, `SELECT count(d1.y), count(d2.y) FROM fact, dim d1, dim d2
	                     WHERE fact.a = d1.k AND fact.b = d2.k`)
	plan, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	agg := aliasCollapseAggNode(t, plan)
	if len(agg.Aggs) != 2 {
		t.Fatalf("planned %d aggregate slots, want 2 (aliases collapsed)", len(agg.Aggs))
	}
	a0, ok0 := agg.Aggs[0].Arg.(*ColumnRef)
	a1, ok1 := agg.Aggs[1].Arg.(*ColumnRef)
	if !ok0 || !ok1 {
		t.Fatalf("agg args are %T/%T, want *ColumnRef", agg.Aggs[0].Arg, agg.Aggs[1].Arg)
	}
	if a0.Index == a1.Index {
		t.Fatalf("both aggregates read child column %d; d1.y and d2.y collapsed", a0.Index)
	}
	got := groupKeySlots(projectTargets(t, plan), 2)
	if got[0] == got[1] {
		t.Fatalf("count(d1.y) and count(d2.y) both project slot %d", got[0])
	}
	if got[0] != 0 || got[1] != 1 {
		t.Fatalf("target slots = %v, want [0 1]", got)
	}
}

// TestAggregateAliasedSelfJoinComputedArgsDistinctSlots pins the collapse one
// level up: sum(d1.y + 0) and sum(d2.y + 0) share the blind key through a
// computed argument, so the fix must not depend on the argument being a bare
// ColumnRef.
func TestAggregateAliasedSelfJoinComputedArgsDistinctSlots(t *testing.T) {
	cat := selfJoinGroupByCatalog(t)
	stmt := parseOne(t, `SELECT sum(d1.y + 0), sum(d2.y + 0) FROM fact, dim d1, dim d2
	                     WHERE fact.a = d1.k AND fact.b = d2.k`)
	plan, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	agg := aliasCollapseAggNode(t, plan)
	if len(agg.Aggs) != 2 {
		t.Fatalf("planned %d aggregate slots, want 2", len(agg.Aggs))
	}
	got := groupKeySlots(projectTargets(t, plan), 2)
	if got[0] == got[1] {
		t.Fatalf("sum(d1.y+0) and sum(d2.y+0) both project slot %d", got[0])
	}
}

// TestAggregateRepeatedCallStillSharesOneSlot guards the dedup this key
// exists to provide: the SAME call written twice (and once more in HAVING)
// is one aggregate, not three. The contested-key path must only split calls
// whose qualified forms differ.
func TestAggregateRepeatedCallStillSharesOneSlot(t *testing.T) {
	cat := selfJoinGroupByCatalog(t)
	stmt := parseOne(t, `SELECT count(d1.y), count(d1.y) FROM fact, dim d1
	                     WHERE fact.a = d1.k HAVING count(d1.y) >= 0`)
	plan, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	agg := aliasCollapseAggNode(t, plan)
	if len(agg.Aggs) != 1 {
		t.Fatalf("planned %d aggregate slots, want 1 (repetition must dedup)", len(agg.Aggs))
	}
	got := groupKeySlots(projectTargets(t, plan), 2)
	if got[0] != got[1] {
		t.Fatalf("identical calls project different slots %v", got)
	}
}

// TestAggregateAliasedSelfJoinWithGroupByAndOrderBy exercises the resolution
// side across clauses: the ORDER BY copy of each call must land on the same
// slot as its SELECT twin, per alias, with a contested GROUP BY key in play
// at the same time (-0044 and -0045 stacked, which is exactly TPC-DS Q64's
// shape).
func TestAggregateAliasedSelfJoinWithGroupByAndOrderBy(t *testing.T) {
	cat := selfJoinGroupByCatalog(t)
	stmt := parseOne(t, `SELECT d1.y, d2.y, count(d1.y), count(d2.y)
	                     FROM fact, dim d1, dim d2
	                     WHERE fact.a = d1.k AND fact.b = d2.k
	                     GROUP BY d1.y, d2.y
	                     ORDER BY count(d2.y), count(d1.y)`)
	plan, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var agg *Aggregate
	for c := plan; agg == nil && c != nil; {
		switch x := c.(type) {
		case *Aggregate:
			agg = x
		case *Project:
			c = x.Child
		case *Sort:
			c = x.Child
		case *Filter:
			c = x.Child
		default:
			t.Fatalf("unexpected node %T while looking for Aggregate", c)
		}
	}
	if len(agg.Aggs) != 2 {
		t.Fatalf("planned %d aggregate slots, want 2", len(agg.Aggs))
	}
	a0, ok0 := agg.Aggs[0].Arg.(*ColumnRef)
	a1, ok1 := agg.Aggs[1].Arg.(*ColumnRef)
	if !ok0 || !ok1 || a0.Index == a1.Index {
		t.Fatalf("aggregate args %v/%v must read distinct child columns", agg.Aggs[0].Arg, agg.Aggs[1].Arg)
	}
}
