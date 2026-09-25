package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// sortedDistinctArm is a sort-based DISTINCT arm: Unique over a Sort on every
// output column ascending, the shape setOpArmSortedAllCols recognises.
func sortedDistinctArm(rows float64, desc bool) *Distinct {
	in := upperOrderedInput(rows)
	cols := in.Output()
	keys := make([]SortKey, len(cols))
	for i, c := range cols {
		keys[i] = SortKey{Expr: &ColumnRef{Index: i, Name: c.Name, Type: c.Type}, Desc: desc && i == 0}
	}
	srt := &Sort{Child: in, Keys: keys}
	return &Distinct{Child: srt, schema: srt.Output()}
}

// TestSortedSetOpCandidate pins M0146-0005q: over two presorted DISTINCT
// arms an INTERSECT / EXCEPT gets PG's SETOP_SORTED candidate — the inputs'
// startups as startup, the hashed arm's total, the ordering as pathkeys — so
// it wins add_path on startup; a hashed arm or a descending key offers none.
func TestSortedSetOpCandidate(t *testing.T) {
	cp := defaultCostParams()
	l, r := sortedDistinctArm(100, false), sortedDistinctArm(50, false)
	if !setOpArmSortedAllCols(l) || setOpArmSortedAllCols(sortedDistinctArm(10, true)) {
		t.Fatal("arm detection: want true for ascending all-column sort, false for a descending key")
	}
	if setOpArmSortedAllCols(&Distinct{Child: upperOrderedInput(10), schema: upperOrderedInput(10).Output()}) {
		t.Fatal("a hashed DISTINCT arm must not count as sorted")
	}
	node := &SetOp{Left: l, Right: r, Op: parser.SetOpIntersect}
	rel := newRelOptInfo(0, 50, 16)
	rel.NCols = 3
	lseed := &Path{Kind: PathPrebuilt, Rows: 100, Cost: Cost{Startup: 10, Total: 100}}
	rseed := &Path{Kind: PathPrebuilt, Rows: 50, Cost: Cost{Startup: 5, Total: 60}}
	addSetOpPaths(rel, lseed, rseed, node, cp)
	if len(rel.Pathlist) != 1 {
		t.Fatalf("pathlist = %d paths, want the sorted path alone (it dominates on startup)", len(rel.Pathlist))
	}
	p := rel.Pathlist[0]
	if p.SetOp == nil || len(p.SetOp.MergeKeys) != 3 || len(p.Pathkeys) != 3 {
		t.Fatalf("winner %+v is not the sorted candidate", p)
	}
	wantTotal := 100 + 60 + cp.cpuOperatorCost*150*3 + cp.cpuOperatorCost*50
	if p.Cost.Startup != 15 || p.Cost.Total != wantTotal {
		t.Fatalf("cost = %+v, want startup 15, total %g", p.Cost, wantTotal)
	}
}

// TestSetOpArmSortedShapes: goopg's sort-based SELECT DISTINCT is a
// DistinctOn over every column above the Sort, and a nested sorted
// INTERSECT/EXCEPT delivers its merge order — both count as presorted arms,
// so a chain like TPC-DS Q38's takes SETOP_SORTED at every link.
func TestSetOpArmSortedShapes(t *testing.T) {
	d := sortedDistinctArm(100, false)
	on := &DistinctOn{Child: d.Child, KeyCols: []int{0, 1, 2}, schema: d.Output()}
	if !setOpArmSortedAllCols(on) {
		t.Fatal("DistinctOn over every column above an all-column Sort must count as sorted")
	}
	partial := &DistinctOn{Child: d.Child, KeyCols: []int{0, 1}, schema: d.Output()}
	if setOpArmSortedAllCols(partial) {
		t.Fatal("DistinctOn over a key prefix is not a whole-row ordering")
	}
	nested := &SetOp{Left: d, Right: d, Op: parser.SetOpIntersect, MergeKeys: distinctAllColKeys(d)}
	if !setOpArmSortedAllCols(nested) {
		t.Fatal("a sorted INTERSECT arm must count as sorted")
	}
	hashed := &SetOp{Left: d, Right: d, Op: parser.SetOpIntersect}
	if setOpArmSortedAllCols(hashed) {
		t.Fatal("a hashed INTERSECT arm is unordered")
	}
}
