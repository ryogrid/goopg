package optimizer

// B-05c tests: planner consumption of multivariate-ndistinct extended
// statistics in the GROUP BY estimate path (estimate_multivariate_ndistinct
// exact-set cut, selfuncs.c:4220).
//
// Oracle behavior ported: per source relation, the registered combo ndistinct
// for the relation's whole GROUP BY column set replaces the independence
// product; everything else (subset, superset, cross-relation, empty registry,
// corrupt values) falls back to the pre-B-05c product arithmetic bit-for-bit.
//
// Fixtures carry real table OIDs and column Ordinals (attnum = Ordinal+1),
// unlike cardinality_numgroups_test.go's OID-less scans — the combo lookup
// keys on (table OID, attnum set), so OID-less fixtures can never hit.

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// mvTable is numGroupsScan with catalog identity: table OID + per-column
// Ordinals, so grouping variables resolve to (tableOID, attnum) pairs the
// combo registry keys on. Columns are named a, b, c, ... like numGroupsScan.
func mvTable(oid uint32, rows int64, ndistinct ...int64) *SeqScan {
	cols := make([]catalog.ColumnStats, len(ndistinct))
	columns := make([]catalog.Column, len(ndistinct))
	for i, nd := range ndistinct {
		cols[i] = catalog.ColumnStats{NDistinct: nd}
		columns[i] = catalog.Column{
			Name:    string(rune('a' + i)),
			Type:    catalog.Type{Name: "int4"},
			Ordinal: i,
		}
	}
	tbl := &catalog.Table{
		Name:    "mv_t",
		OID:     oid,
		Columns: columns,
		Stats:   &catalog.TableStats{RowCount: rows, Columns: cols},
	}
	return &SeqScan{Table: tbl, schema: tableSchema(tbl)}
}

func registerMVND(t *testing.T, tableOID uint32, keys []int16, items ...PlannerExtNDistinctItem) {
	t.Helper()
	t.Cleanup(ClearPlannerExtStats)
	RegisterPlannerExtStats(tableOID, PlannerExtStatsObject{
		StatsOID:  8100 + tableOID,
		Keys:      keys,
		NDistinct: items,
	})
}

// Exact-set hit: GROUP BY a, b over (200 x 100, 1000 rows) prices 200 today
// (product 20 000 -> tuples/10 = 100 -> floored at max(nd) = 200); the
// registered combo 37 replaces it. GROUP BY order must not matter.
func TestMultivariateNDistinctExactHit(t *testing.T) {
	scan := mvTable(8001, 1000, 200, 100)
	registerMVND(t, 8001, []int16{1, 2}, PlannerExtNDistinctItem{NDistinct: 37, Attrs: []int16{1, 2}})
	if got := estimateNumGroups([]Expr{jrCol(0), jrCol(1)}, scan, 100000); got != 37 {
		t.Fatalf("groups = %d, want 37 (registered combo ndistinct)", got)
	}
	if got := estimateNumGroups([]Expr{jrCol(1), jrCol(0)}, scan, 100000); got != 37 {
		t.Fatalf("reversed GROUP BY order: groups = %d, want 37 (set match, not sequence)", got)
	}
}

// The hit still respects the closing input-rows clamp: 37 combos cannot
// appear among 5 rows.
func TestMultivariateNDistinctHitClampsToInputRows(t *testing.T) {
	scan := mvTable(8002, 1000, 200, 100)
	registerMVND(t, 8002, []int16{1, 2}, PlannerExtNDistinctItem{NDistinct: 37, Attrs: []int16{1, 2}})
	if got := estimateNumGroups([]Expr{jrCol(0), jrCol(1)}, scan, 5); got != 5 {
		t.Fatalf("groups = %d, want 5 (combo 37 clamped to input rows)", got)
	}
}

// Subset: combo covers {a,b} but the GROUP BY is {a,b,c} — no exact item, so
// the full independence product stands (200*100*50 -> clamp 100 -> floor
// max 200 -> 200). Upstream would price ndistinct(a,b)*ndistinct(c); the
// iterative remainder is the documented resume point, not faked here.
func TestMultivariateNDistinctSubsetFallsBack(t *testing.T) {
	scan := mvTable(8003, 1000, 200, 100, 50)
	registerMVND(t, 8003, []int16{1, 2}, PlannerExtNDistinctItem{NDistinct: 37, Attrs: []int16{1, 2}})
	got := estimateNumGroups([]Expr{jrCol(0), jrCol(1), jrCol(2)}, scan, 100000)
	if want := int64(200); got != want {
		t.Fatalf("groups = %d, want %d (subset: full product path)", got, want)
	}
}

// Superset: only a {a,b,c} combo is registered but the GROUP BY is {a,b} —
// no exact item, product path (200).
func TestMultivariateNDistinctSupersetFallsBack(t *testing.T) {
	scan := mvTable(8004, 1000, 200, 100, 50)
	registerMVND(t, 8004, []int16{1, 2, 3}, PlannerExtNDistinctItem{NDistinct: 111, Attrs: []int16{1, 2, 3}})
	got := estimateNumGroups([]Expr{jrCol(0), jrCol(1)}, scan, 100000)
	if want := int64(200); got != want {
		t.Fatalf("groups = %d, want %d (superset: full product path)", got, want)
	}
	// And the single-key arm never consults combos: GROUP BY a is 200 with
	// or without the registered object.
	if got := estimateNumGroups([]Expr{jrCol(0)}, scan, 100000); got != 200 {
		t.Fatalf("single-key groups = %d, want 200 (combos cover k>=2 only)", got)
	}
}

// Empty registry: the combo path declines and every shape prices exactly its
// pre-B-05c value. This is also the production state until the 3429 loader
// lands (no loader populates the registry outside tests).
func TestMultivariateNDistinctEmptyRegistryIdentical(t *testing.T) {
	t.Cleanup(ClearPlannerExtStats)
	scan := mvTable(8005, 1000, 200, 100, 50)
	if got, want := estimateNumGroups([]Expr{jrCol(0), jrCol(1)}, scan, 100000), int64(200); got != want {
		t.Fatalf("two-key groups = %d, want %d (product path, no registry)", got, want)
	}
	if got, want := estimateNumGroups([]Expr{jrCol(0), jrCol(1), jrCol(2)}, scan, 100000), int64(200); got != want {
		t.Fatalf("three-key groups = %d, want %d (product path, no registry)", got, want)
	}
	if got, want := estimateNumGroups([]Expr{jrCol(0)}, scan, 100000), int64(200); got != want {
		t.Fatalf("single-key groups = %d, want %d (column ndistinct, no registry)", got, want)
	}
}

// Cross-relation sets never consult combos: A.a and B.b sit in different
// per-relation groups (step 5 multiplies), each a singleton that declines
// the k>=2 gate on its own — even with a combo registered on A.
func TestMultivariateNDistinctCrossRelationFallsBack(t *testing.T) {
	a := mvTable(8006, 100, 10, 7)
	b := mvTable(8007, 1000, 20)
	registerMVND(t, 8006, []int16{1, 2}, PlannerExtNDistinctItem{NDistinct: 5, Attrs: []int16{1, 2}})
	j := mergedJoin(JoinTypeInner, a, b)
	lw := len(a.Output())
	got := estimateNumGroups([]Expr{jrCol(0), jrCol(lw)}, j, 100000)
	if want := int64(200); got != want {
		t.Fatalf("groups = %d, want %d (10 x 20 across relations, no combo)", got, want)
	}
}

// Self-join: two instances of one table OID group by scan instance, so a
// combo on the shared OID never sees a merged cross-instance set.
func TestMultivariateNDistinctSelfJoinFallsBack(t *testing.T) {
	n1 := mvTable(8008, 100, 10, 9)
	n2 := mvTable(8008, 100, 10, 9)
	registerMVND(t, 8008, []int16{1, 2}, PlannerExtNDistinctItem{NDistinct: 5, Attrs: []int16{1, 2}})
	j := mergedJoin(JoinTypeInner, n1, n2)
	lw := len(n1.Output())
	got := estimateNumGroups([]Expr{jrCol(0), jrCol(lw)}, j, 100000)
	if want := int64(100); got != want {
		t.Fatalf("groups = %d, want %d (10 x 10 across instances, no combo)", got, want)
	}
}

// Corrupt-registry values (NaN, zero, negative) cannot come out of B-05a's
// Duj1 builder; a hand-populated registry smuggling one in falls back to the
// product rather than collapsing the estimate.
func TestMultivariateNDistinctInvalidValueFallsBack(t *testing.T) {
	for _, bad := range []float64{math.NaN(), 0, -3} {
		scan := mvTable(8009, 1000, 200, 100)
		t.Cleanup(ClearPlannerExtStats)
		RegisterPlannerExtStats(8009, PlannerExtStatsObject{
			StatsOID:  8910,
			Keys:      []int16{1, 2},
			NDistinct: []PlannerExtNDistinctItem{{NDistinct: bad, Attrs: []int16{1, 2}}},
		})
		got := estimateNumGroups([]Expr{jrCol(0), jrCol(1)}, scan, 100000)
		if want := int64(200); got != want {
			t.Fatalf("ndistinct=%v: groups = %d, want %d (invalid combo falls back)", bad, got, want)
		}
	}
}

// The *Aggregate node feeds the combo through the same estimateNumGroups it
// always ran — this is the wiring production actually executes.
func TestMultivariateNDistinctThroughAggregate(t *testing.T) {
	scan := mvTable(8010, 1000, 200, 100)
	registerMVND(t, 8010, []int16{1, 2}, PlannerExtNDistinctItem{NDistinct: 37, Attrs: []int16{1, 2}})
	agg := &Aggregate{Child: scan, GroupExprs: []Expr{jrCol(0), jrCol(1)}}
	if got := EstimateRows(agg); got != 37 {
		t.Fatalf("EstimateRows(Aggregate) = %d, want 37 (combo through the node)", got)
	}
}
