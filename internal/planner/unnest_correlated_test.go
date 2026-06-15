package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanUnnestCorrelatedArg reproduces DU-002 slice 23: a FROM-clause
// unnest() SRF whose array argument is a CORRELATED reference to the outer
// query level (inside a scalar/ARRAY subquery). pg_dump's getEventTriggers
// expands evttags this way:
//
//	array(select quote_literal(x) from unnest(evttags) as t(x))
//
// Before the fix, planFromUnnest built its arg-resolution context from the
// same-level lateral siblings only, never chaining up to planParent, so the
// correlated arg failed with 42703 "column ... does not exist".
func TestPlanUnnestCorrelatedArg(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "et"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "tags", Type: catalog.Type{Name: "text[]"}},
	}); err != nil {
		t.Fatal(err)
	}

	// ARRAY subquery with a correlated unnest arg (the pg_dump shape).
	q := `SELECT id, array(select x from unnest(tags) as t(x)) FROM et`
	if _, err := Plan(parseOne(t, q), c); err != nil {
		t.Fatalf("correlated ARRAY unnest arg failed to plan: %v", err)
	}

	// Scalar subquery with a correlated unnest arg.
	q2 := `SELECT id, (SELECT count(*) FROM unnest(tags)) FROM et`
	if _, err := Plan(parseOne(t, q2), c); err != nil {
		t.Fatalf("correlated scalar unnest arg failed to plan: %v", err)
	}

	// Same-level explicit LATERAL must keep working.
	q3 := `SELECT id, u.tag FROM et, LATERAL unnest(tags) AS u(tag)`
	if _, err := Plan(parseOne(t, q3), c); err != nil {
		t.Fatalf("same-level LATERAL unnest arg failed to plan: %v", err)
	}
}
