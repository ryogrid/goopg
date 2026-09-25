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

// TestSortedSetOpArmRebuildsUnique pins M0146-0005s: a hashed DISTINCT arm
// still offers the sorted SetOp its Sort + Unique form (PG's arm rel keeps
// that path for its pathkeys), a presorted arm is used as is, and an arm with
// no DISTINCT has no sorted form.
func TestSortedSetOpArmRebuildsUnique(t *testing.T) {
	cp := defaultCostParams()
	rel := newRelOptInfo(0, 50, 16)
	in := upperOrderedInput(1000)
	hashed := &Distinct{Child: in, schema: in.Output()}
	node, p := sortedSetOpArm(rel, hashed, &Path{Kind: PathPrebuilt, Rows: 100}, cp)
	on, ok := node.(*DistinctOn)
	if !ok || p == nil {
		t.Fatalf("hashed arm: got %T, want the rebuilt Unique", node)
	}
	if _, isSort := on.Child.(*Sort); !isSort || !setOpArmSortedAllCols(on) {
		t.Fatalf("rebuilt arm is not a Unique over an all-column Sort: child %T", on.Child)
	}
	if p.Cost.Startup <= 0 || p.Cost.Total < p.Cost.Startup {
		t.Fatalf("rebuilt arm carries no Sort cost: %+v", p.Cost)
	}
	sorted := sortedDistinctArm(100, false)
	seed := &Path{Kind: PathPrebuilt, Rows: 100}
	if n, sp := sortedSetOpArm(rel, sorted, seed, cp); n != Node(sorted) || sp != seed {
		t.Fatal("a presorted arm must be used as is")
	}
	if n, sp := sortedSetOpArm(rel, in, seed, cp); n != nil || sp != nil {
		t.Fatal("an arm without DISTINCT has no sorted form")
	}
}

// TestSwapIntersectInputs pins M0146-0005r: generate_nonunion_paths puts the
// INTERSECT input with fewer groups on the left, keeping the written first
// arm's column names; EXCEPT is never swapped.
func TestSwapIntersectInputs(t *testing.T) {
	big := &SeqScan{Table: statsTable("big", 1000, 1000)}
	small := &SeqScan{Table: statsTable("small", 400, 400)}
	n := &SetOp{Left: big, Right: small, Op: parser.SetOpIntersect}
	sw := swapIntersectInputs(n)
	if sw.Left != Node(small) || sw.Right != Node(big) {
		t.Fatal("INTERSECT with the larger arm first must swap")
	}
	want, got := n.Output(), sw.Output()
	if len(want) != len(got) || (len(want) > 0 && want[0].Name != got[0].Name) {
		t.Fatalf("swapped output %v must keep the written first arm's columns %v", got, want)
	}
	if swapIntersectInputs(&SetOp{Left: small, Right: big, Op: parser.SetOpIntersect}).Left != Node(small) {
		t.Fatal("the smaller arm already first must stay")
	}
	ex := &SetOp{Left: big, Right: small, Op: parser.SetOpExcept}
	if swapIntersectInputs(ex) != ex {
		t.Fatal("EXCEPT must never swap")
	}
}
