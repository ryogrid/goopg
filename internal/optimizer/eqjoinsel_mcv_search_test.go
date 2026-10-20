package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestSearchEqJoinUsesBothSidesMCVLists pins M0146-0230: the join search's
// per-clause estimator takes eqjoinsel_inner's MCV branch (selfuncs.c) when
// both operands carry an MCV list, as the plan-node estimator already did.
//
// Hand-computed from upstream's formula, independently of the code:
//
//	side 1: MCV {a 0.5, b 0.3}, nd 3, nullfrac 0 → other1 0.2
//	side 2: MCV {a 0.4, c 0.4}, nd 4, nullfrac 0 → other2 0.2
//	matchprodfreq = 0.5*0.4 = 0.2 (one match), unmatch1 0.3, unmatch2 0.4
//	totalsel1 = 0.2 + 0.3*0.2/(4-2) + 0.2*(0.2+0.4)/(4-1) = 0.27
//	totalsel2 = 0.2 + 0.4*0.2/(3-2) + 0.2*(0.2+0.3)/(3-1) = 0.33
//	selec = min = 0.27 (the no-MCV branch would give 1/max(nd) = 0.25)
//
// TPC-DS Q64 at SF0.25 is the record: its cd1.cd_marital_status <>
// cd2.cd_marital_status join came out 1536640 rows (1 - 1/5) where PG has
// 1536621 (1 - the MCV-matched eqjoinsel).
func TestSearchEqJoinUsesBothSidesMCVLists(t *testing.T) {
	v1 := joinVarStats{tuples: 100, stats: &catalog.ColumnStats{NDistinct: 3,
		MCV: []catalog.MCVEntry{{Value: "a", Frequency: 0.5}, {Value: "b", Frequency: 0.3}}}}
	v2 := joinVarStats{tuples: 100, stats: &catalog.ColumnStats{NDistinct: 4,
		MCV: []catalog.MCVEntry{{Value: "a", Frequency: 0.4}, {Value: "c", Frequency: 0.4}}}}
	got, isdefault := eqJoinSelectivityExt(v1, v2)
	if math.Abs(got-0.27) > 1e-12 || isdefault {
		t.Fatalf("eqJoinSelectivityExt with both MCV lists = %.15f (isdefault=%v), want 0.27 measured", got, isdefault)
	}
	// The plan-node twin runs the same core and must agree.
	core, ok := eqjoinselInnerMCVCore(v1.stats, v2.stats, 3, 4, false)
	if !ok || math.Abs(core-got) > 1e-15 {
		t.Fatalf("plan-node core = %.15f ok=%v, search = %.15f: the two estimators disagree", core, ok, got)
	}
	// One side without an MCV list keeps the no-MCV branch.
	v2.stats = &catalog.ColumnStats{NDistinct: 4}
	if got, _ := eqJoinSelectivityExt(v1, v2); math.Abs(got-0.25) > 1e-12 {
		t.Fatalf("one-sided MCV: got %.15f, want 1/max(nd) = 0.25", got)
	}
	// bpchar pairs on trimmed values (bpchareq).
	b1 := joinVarStats{tuples: 100, typeName: "bpchar", stats: &catalog.ColumnStats{NDistinct: 3,
		MCV: []catalog.MCVEntry{{Value: "a   ", Frequency: 0.5}, {Value: "b", Frequency: 0.3}}}}
	b2 := joinVarStats{tuples: 100, typeName: "bpchar", stats: &catalog.ColumnStats{NDistinct: 4,
		MCV: []catalog.MCVEntry{{Value: "a", Frequency: 0.4}, {Value: "c", Frequency: 0.4}}}}
	if got, _ := eqJoinSelectivityExt(b1, b2); math.Abs(got-0.27) > 1e-12 {
		t.Fatalf("bpchar MCV pairing: got %.15f, want 0.27", got)
	}
}
