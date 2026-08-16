package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanPgOptionsToTableCorrelatedArg reproduces DU-002 slice 18:
// a FROM-clause pg_options_to_table() SRF whose array argument is a
// CORRELATED reference to the outer query level (inside a scalar/ARRAY
// subquery). pg_dump's getForeignDataWrappers expands fdwoptions this way.
func TestPlanPgOptionsToTableCorrelatedArg(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "fdw"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "opts", Type: catalog.Type{Name: "text[]"}},
	}); err != nil {
		t.Fatal(err)
	}

	// ARRAY subquery with a correlated SRF arg.
	q := `SELECT id, ARRAY(SELECT option_name FROM pg_options_to_table(opts) ORDER BY option_name) FROM fdw`
	if _, err := Plan(parseOne(t, q), c); err != nil {
		t.Fatalf("correlated ARRAY pg_options_to_table arg failed to plan: %v", err)
	}

	// Scalar subquery with a correlated SRF arg.
	q2 := `SELECT id, (SELECT count(*) FROM pg_options_to_table(opts)) FROM fdw`
	if _, err := Plan(parseOne(t, q2), c); err != nil {
		t.Fatalf("correlated scalar pg_options_to_table arg failed to plan: %v", err)
	}

	// Same-level explicit LATERAL must keep working.
	q3 := `SELECT id, x.option_name FROM fdw, LATERAL pg_options_to_table(opts) x`
	if _, err := Plan(parseOne(t, q3), c); err != nil {
		t.Fatalf("same-level LATERAL pg_options_to_table arg failed to plan: %v", err)
	}
}
