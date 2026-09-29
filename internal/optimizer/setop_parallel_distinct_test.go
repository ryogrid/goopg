package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestSetOpArmSortedThroughGatherMerge pins M0146-0005bg: a parallel
// DISTINCT arm lowers to `Unique -> Gather Merge -> Unique -> Sort`, and the
// leader Unique's input is merged on every output column, so the arm is as
// sorted as a Unique over a Sort. generate_nonunion_paths then offers PG's
// SETOP_SORTED candidate over it (TPC-DS Q38/Q87: `SetOp Intersect` /
// `SetOp Except` in PG 18.3, where goopg only offered HashSetOp).
func TestSetOpArmSortedThroughGatherMerge(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	sch := Schema{{Name: "a", Type: int4}, {Name: "b", Type: int4}}
	scan := &Values{schema: sch}
	keys := distinctAllColKeys(scan)
	worker := &DistinctOn{Child: &Sort{Child: scan, Keys: keys}, KeyCols: []int{0, 1}, schema: sch, PartialUnique: true}
	gm := &GatherMerge{Child: worker, WorkersPlanned: 2, Keys: keys, schema: sch}
	leader := &DistinctOn{Child: gm, KeyCols: []int{0, 1}, schema: sch}
	if !setOpArmSortedAllCols(leader) {
		t.Fatalf("a Unique over an all-column Gather Merge is sorted on all columns")
	}
	gm.Keys = keys[:1]
	if setOpArmSortedAllCols(leader) {
		t.Fatalf("a Gather Merge on a prefix of the columns is not")
	}
}

// TestEstimateRowsReadsStampedDistinctRows pins M0146-0005bg: a DISTINCT node
// the path search built carries the DISTINCT rel's row count, and every later
// reader sees that number rather than a re-estimate over the lowered child;
// a per-worker stamp is not a whole-node count and is ignored.
func TestEstimateRowsReadsStampedDistinctRows(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	sch := Schema{{Name: "a", Type: int4}}
	d := &DistinctOn{Child: &Values{schema: sch}, KeyCols: []int{0}, schema: sch}
	d.setPlanCost(PlanCost{PlanRows: 3548, TotalCost: 1})
	if got := EstimateRows(d); got != 3548 {
		t.Errorf("stamped DISTINCT rows: got %d, want 3548", got)
	}
	d.setPlanCost(PlanCost{PlanRows: 1093, TotalCost: 1, PerWorker: true})
	if got := EstimateRows(d); got == 1093 {
		t.Errorf("a per-worker stamp must not be read as the node's rows")
	}
}
