package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestBuiltinAggCostTableAgreesWithPgAggregate checks the planner's aggregate
// cost table (catalog.builtinAggCosts, generated from pg_aggregate.dat by
// cmd/gen-pg-proc-data -aggcost, M0146-0221) against the pg_aggregate rows
// initdb bootstraps. The two are separate copies of the same catalog, because
// the optimizer cannot import initdb. If they disagree on which support
// functions an aggregate has, the planner prices a plan the catalog cannot
// execute that way.
func TestBuiltinAggCostTableAgreesWithPgAggregate(t *testing.T) {
	entries := pgAggregateInitialEntries()
	if got := catalog.BuiltinAggCostCount(); got != len(entries) {
		t.Errorf("cost table has %d aggregates, pg_aggregate bootstrap has %d", got, len(entries))
	}
	for _, e := range entries {
		c, ok := catalog.BuiltinAggCostByOID(e.AggFnOID)
		if !ok {
			t.Errorf("aggfnoid %d: no cost-table row", e.AggFnOID)
			continue
		}
		pairs := []struct {
			fn   string
			oid  uint32
			cost float64
		}{
			{"finalfn", e.AggFinalFn, c.Final},
			{"combinefn", e.AggCombineFn, c.Combine},
			{"serialfn", e.AggSerialFn, c.Serial},
			{"deserialfn", e.AggDeserialFn, c.Deserial},
			{"transfn", e.AggTransFn, c.Trans},
		}
		for _, p := range pairs {
			if (p.oid != 0) != (p.cost != 0) {
				t.Errorf("aggfnoid %d: %s oid %d but cost %v", e.AggFnOID, p.fn, p.oid, p.cost)
			}
		}
	}
}
