package optimizer

import (
	"testing"
)

// M0144-0011b-1 guards. The claim under test is narrow and falsifiable in two
// independent halves, so they are two tests:
//
//  1. WHICH leaves are sub-plan leaves — the classification must admit exactly
//     the finished-subtree classes and must leave every base-table access on
//     the pricing it had, so a category move is attributable.
//  2. WHAT a sub-plan leaf costs — `cost_subqueryscan`'s shape, starting from
//     the subtree rather than from a page count invented from the row count
//     (`postgres/src/backend/optimizer/path/costsize.c:1491-1493`).
//
// The defect these pin: TPC-DS SF0.25 Q8 entered a `HashSetOp Intersect`
// subtree estimated at 7360.42 into the join search at 8.35, and the hash join
// above it printed 11.28 — a parent 650x cheaper than its own child.

// subplanLeafFixture builds a two-child SetOp over priced children, the shape
// Q8's leaf actually has: the children carry REAL search costs and the SetOp
// itself has no `PlanCost` field, so its cost comes from
// `DeriveLegacyDisplayCost`'s pass-through arm.
func subplanLeafFixture(leftTotal, rightTotal float64) *SetOp {
	mk := func(total float64) *SeqScan {
		s := mkSeqScanLeaf("t", 100)
		s.setPlanCost(PlanCost{StartupCost: 0, TotalCost: total, PlanRows: 100, PlanWidth: 8})
		return s
	}
	return &SetOp{Left: mk(leftTotal), Right: mk(rightTotal)}
}

// TestIsSubplanLeafAdmitsOnlyFinishedSubtrees: every base-table access keeps
// today's pricing. That restriction is what makes the parity movement of this
// change attributable to sub-plan leaves alone — an index or bitmap leaf is
// mispriced too, but by a different mechanism with a different PG function
// (`cost_index`), and it is filed rather than folded in.
func TestIsSubplanLeafAdmitsOnlyFinishedSubtrees(t *testing.T) {
	tbl := statsTable("t", 100, 100)
	sch := tableSchema(tbl)

	baseAccess := []struct {
		name string
		leaf Node
	}{
		{"SeqScan", &SeqScan{Table: tbl, schema: sch}},
		{"IndexScan", &IndexScan{Table: tbl, schema: sch}},
		{"IndexOnlyScan", &IndexOnlyScan{Table: tbl, schema: sch}},
		{"BitmapHeapScan", &BitmapHeapScan{Table: tbl, schema: sch}},
	}
	for _, c := range baseAccess {
		if isSubplanLeaf(c.leaf) {
			t.Errorf("%s classified as a sub-plan leaf; base-table accesses must keep their pricing", c.name)
		}
		// A local filter wrapping the access must not change the answer:
		// `leafBaseScan` descends through `*Filter` and the classification
		// reads the access underneath it.
		if isSubplanLeaf(&Filter{Child: c.leaf}) {
			t.Errorf("Filter{%s} classified as a sub-plan leaf", c.name)
		}
	}

	subplans := []struct {
		name string
		leaf Node
	}{
		{"SetOp", subplanLeafFixture(10, 20)},
		{"CTEScan", &CTEScan{}},
		{"an already-built join subtree", &Join{}},
		{"an aggregate subtree", &Aggregate{}},
	}
	for _, c := range subplans {
		if !isSubplanLeaf(c.leaf) {
			t.Errorf("%s not classified as a sub-plan leaf", c.name)
		}
	}

	if isSubplanLeaf(nil) {
		t.Error("a nil leaf must not be classified as a sub-plan leaf")
	}
}

// TestCostSubplanLeafStartsFromTheSubtree is the pricing half. PG's
// `cost_subqueryscan` assigns the subpath's costs and then charges
// `cpu_tuple_cost` per row; the one thing it never does is re-derive the
// child's work. So the leaf's total must be STRICTLY ABOVE its subtree's
// total, and must move when the subtree moves.
func TestCostSubplanLeafStartsFromTheSubtree(t *testing.T) {
	cp := defaultCostParams()
	const rows = 535.0

	leaf := subplanLeafFixture(1000, 2000)
	sub := legacyDisplayCostOf(leaf)
	got := costSubplanLeaf(cp, leaf, rows)

	if got.Startup != sub.StartupCost {
		t.Errorf("startup = %v, want the subtree's %v (costsize.c:1492)", got.Startup, sub.StartupCost)
	}
	want := sub.TotalCost + cp.cpuTupleCost*rows
	if got.Total != want {
		t.Errorf("total = %v, want subtree %v + cpu_tuple_cost*%v = %v", got.Total, sub.TotalCost, rows, want)
	}
	if got.Total <= sub.TotalCost {
		t.Fatalf("total %v is not above the subtree's %v — the monotonicity violation this fixes", got.Total, sub.TotalCost)
	}

	// The subtree's cost must REACH the leaf: a dearer subtree is a dearer
	// leaf. Before this change the leaf's cost was a function of the row
	// count alone, so this is the property that actually failed.
	dearer := costSubplanLeaf(cp, subplanLeafFixture(100000, 200000), rows)
	if dearer.Total <= got.Total {
		t.Fatalf("a 100x dearer subtree priced %v, not above the cheaper one's %v", dearer.Total, got.Total)
	}

	// Guard the arithmetic against a negative row estimate rather than
	// letting it subtract from the subtree's cost.
	if neg := costSubplanLeaf(cp, leaf, -5); neg.Total != sub.TotalCost {
		t.Errorf("negative rows charged %v, want the bare subtree cost %v", neg.Total, sub.TotalCost)
	}
}

// TestCostSubplanLeafBeatsTheSeqScanFabricationAtQ8Numbers pins the defect
// with the numbers that were actually measured, so a regression reads as the
// original symptom rather than as an abstract inequality. Q8's set-op leaf:
// 535 rows, subtree estimated at 7360.42, entered the search at 8.35.
func TestCostSubplanLeafBeatsTheSeqScanFabricationAtQ8Numbers(t *testing.T) {
	cp := defaultCostParams()
	const (
		rows        = 535.0
		width       = 32
		subtreeCost = 7360.42
	)
	leaf := &SetOp{Left: func() *SeqScan {
		s := mkSeqScanLeaf("t", 100)
		s.setPlanCost(PlanCost{TotalCost: subtreeCost, PlanRows: rows, PlanWidth: width})
		return s
	}()}

	fabricated := costSeqscan(cp, estScanPages(rows, width), rows, 0)
	priced := costSubplanLeaf(cp, leaf, rows)

	if fabricated.Total >= subtreeCost {
		t.Fatalf("fixture no longer reproduces the defect: the seq-scan fabrication priced %v, not below the subtree's %v",
			fabricated.Total, subtreeCost)
	}
	if priced.Total < subtreeCost {
		t.Fatalf("priced leaf %v is still below its own subtree %v", priced.Total, subtreeCost)
	}
}
