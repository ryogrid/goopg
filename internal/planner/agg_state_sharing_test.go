package planner

// M0125-0024's VALUE gate for the aggregate state-sharing path.
//
// docs/design/0125-0024-expression-identity-collisions.md §5.
//
// This is the consequence half of expr_identity_test.go: it asserts the
// planner's actual decision, not just the key. A row-count gate cannot see
// this defect — the wrong answer has exactly the right shape, one row per
// group with a plausible number in it (the SF0.5 oracle's blindness to Q87 in
// M0125-0006 is the same lesson), so the assertion has to be on the plan.
//
// The mechanism it protects: buildAggregate gives two user-aggregate calls one
// SharedStateSlot when sfunc/stype/initcond/distinct and the CONTENT KEYS of
// their argument and FILTER all agree. The executor then designates a leader
// and copies its finished state to every follower
// (operators_join_agg.go:1699-1760), so a key collision makes the second
// aggregate report the first one's value.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// userAggCatalog seeds one table and one user-defined aggregate. Only
// user-defined aggregates share state (buildAggregate sets
// SharedStateSlot = -1 for built-ins), so a built-in cannot exercise this path.
func userAggCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "agg_t"}, []catalog.Column{
		{Name: "flag", Type: catalog.Type{Name: "bool"}, Ordinal: 0},
		{Name: "a", Type: catalog.Type{Name: "int8"}, Ordinal: 1},
		{Name: "b", Type: catalog.Type{Name: "int8"}, Ordinal: 2},
	}); err != nil {
		t.Fatal(err)
	}
	c.RegisterUserAggregate(&catalog.UserAggregate{
		Name:     "ua_sum",
		ArgTypes: []string{"int8"},
		SType:    "int8",
		SFunc:    "int8pl",
		InitCond: "0",
	})
	return c
}

// aggregateNodeOf plans sql and returns the first Aggregate node it finds.
func aggregateNodeOf(t *testing.T, cat catalog.Catalog, sql string) *Aggregate {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	// Package planner has no generic Node walker (Node is Pos()+Output()
	// only), so descend the wrappers a bare aggregate query can produce.
	for n := node; n != nil; {
		switch x := n.(type) {
		case *Aggregate:
			return x
		case *Project:
			n = x.Child
		case *Filter:
			n = x.Child
		case *Sort:
			n = x.Child
		case *Limit:
			n = x.Child
		default:
			t.Fatalf("no Aggregate node in the plan for %q (stopped at %T)", sql, n)
		}
	}
	t.Fatalf("no Aggregate node in the plan for %q", sql)
	return nil
}

func TestSharedStateSlotSeparatesDistinctArgs(t *testing.T) {
	cat := userAggCatalog(t)

	// The two arguments are DIFFERENT expressions of one Go type
	// (*CaseExpr), which the pre-M0125-0024 planExprContentKey keyed as the
	// bare string "*planner.CaseExpr". Both calls were therefore given one
	// SharedStateSlot, and the executor's follower copy made the second
	// column report the first one's total.
	agg := aggregateNodeOf(t, cat, `
		SELECT ua_sum(CASE WHEN flag THEN a ELSE 0 END),
		       ua_sum(CASE WHEN flag THEN b ELSE 0 END)
		FROM agg_t`)
	if len(agg.Aggs) != 2 {
		t.Fatalf("expected 2 aggregate calls, got %d", len(agg.Aggs))
	}
	for i, c := range agg.Aggs {
		if c.UserAgg == nil {
			t.Fatalf("call %d is not a user aggregate — the state-sharing path is not exercised", i)
		}
	}
	if agg.Aggs[0].SharedStateSlot == agg.Aggs[1].SharedStateSlot {
		t.Errorf("both calls got SharedStateSlot=%d: sfunc runs once and the second "+
			"aggregate reports the first's value — the M0097-0032 wrong answer, "+
			"reached here through an identity-key collision on *CaseExpr",
			agg.Aggs[0].SharedStateSlot)
	}
}

// The optimisation must survive the fix: arguments that really are the same
// expression still share one slot, so sfunc's side effects (the NOTICE-emitting
// sfuncs M0097-0035 was filed for) fire once.
func TestSharedStateSlotStillSharesEqualArgs(t *testing.T) {
	cat := userAggCatalog(t)
	agg := aggregateNodeOf(t, cat, `
		SELECT ua_sum(CASE WHEN flag THEN a ELSE 0 END),
		       ua_sum(CASE WHEN flag THEN a ELSE 0 END)
		FROM agg_t`)
	if len(agg.Aggs) < 2 {
		// The two calls are textually identical, so an earlier dedup pass
		// may legitimately collapse them into one planned call; that is a
		// stronger form of sharing, not a failure.
		t.Skipf("planner deduplicated the two identical calls into %d", len(agg.Aggs))
	}
	if agg.Aggs[0].SharedStateSlot != agg.Aggs[1].SharedStateSlot {
		t.Errorf("identical arguments got slots %d and %d — state sharing is dead, "+
			"so a side-effecting sfunc now runs twice per row",
			agg.Aggs[0].SharedStateSlot, agg.Aggs[1].SharedStateSlot)
	}
}
