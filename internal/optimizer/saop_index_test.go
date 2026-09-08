package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// B-14 (P2-09a) — ScalarArrayOp index path fixtures.
//
// Moved shapes: a bare `indexed_col IN (consts)` / `indexed_col = ANY (...)`
// probes the index (IndexScan.SAOPKeys, one descent per element).
// Unmoved shapes: anything outside `match_saopclause_to_indexcol`'s gates
// (indxpath.c:3136 — useOr-only, left indexkey, const array, opfamily)
// stays on SeqScan. These are unit (planner-shape) proofs, not bench runs:
// the Q45 gate shape (10-element PK probe with `= ANY` cond) is covered by
// TestSAOPQ45ShapeMoves, and the must-not-move Q12/Q19/Q22/Q8 family
// (expression operands, unindexed columns) by the unmoved pins.

// findIndexScan returns the first *IndexScan under n (through the wrapper
// nodes planContainsIndexScan sees through, plus Join/NLI children for the
// multi-table rewrite pin), or nil.
func findIndexScan(n Node) *IndexScan {
	if n == nil {
		return nil
	}
	switch v := n.(type) {
	case *IndexScan:
		return v
	case *Project:
		return findIndexScan(v.Child)
	case *Filter:
		return findIndexScan(v.Child)
	case *Sort:
		return findIndexScan(v.Child)
	case *Limit:
		return findIndexScan(v.Child)
	case *Join:
		if s := findIndexScan(v.Left); s != nil {
			return s
		}
		return findIndexScan(v.Right)
	case *NestedLoopIndexJoin:
		if s := findIndexScan(v.Outer); s != nil {
			return s
		}
		if v.Inner != nil {
			return findIndexScan(v.Inner)
		}
	}
	return nil
}

// saopFixture builds the B-14 fixture catalog: an `item`-like table with a
// single-column btree PK on id, a varchar btree index on name, and an
// unindexed flag column — plus a second table for the multi-table
// (rewrite-pass) shape.
func saopFixture(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	item, err := c.CreateTable(parser.ObjectName{Name: "item"}, []catalog.Column{
		{Name: "i_item_sk", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "i_item_id", Type: catalog.Type{Name: "varchar"}, NotNull: false},
		{Name: "i_flag", Type: catalog.Type{Name: "int4"}, NotNull: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "item_pkey"},
		item, []string{"i_item_sk"}, true, "btree", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "idx_item_id"},
		item, []string{"i_item_id"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	// Composite index leading with i_item_sk: the SAOP probe binds its
	// leading column (prefix descent, M0053-0001 rule). Registered AFTER
	// item_pkey so the single-column index stays preferred.
	if _, err := c.CreateIndex(parser.ObjectName{Name: "idx_item_sk_flag"},
		item, []string{"i_item_sk", "i_flag"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	store, err := c.CreateTable(parser.ObjectName{Name: "store"}, []catalog.Column{
		{Name: "s_sk", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "s_name", Type: catalog.Type{Name: "varchar"}, NotNull: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "store_pkey"},
		store, []string{"s_sk"}, true, "btree", false); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSAOPMovedShapes(t *testing.T) {
	c := saopFixture(t)
	cases := []struct {
		name  string
		sql   string
		wantN int
	}{
		{"int PK IN", "SELECT i_item_id FROM item WHERE i_item_sk IN (2, 3, 5)", 3},
		{"varchar IN", "SELECT i_item_sk FROM item WHERE i_item_id IN ('A', 'B')", 2},
		{"any array spelling", "SELECT i_item_id FROM item WHERE i_item_sk = ANY (ARRAY[2, 3])", 2},
		{"single element", "SELECT i_item_id FROM item WHERE i_item_sk IN (7)", 1},
		{"null element kept", "SELECT i_item_id FROM item WHERE i_item_sk IN (7, NULL)", 2},
		{"duplicate elements kept", "SELECT i_item_id FROM item WHERE i_item_sk IN (7, 7, 8)", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := Plan(parseOne(t, tc.sql), c)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			scan := findIndexScan(node)
			if scan == nil {
				t.Fatalf("%s: want IndexScan, got plan without one", tc.sql)
			}
			if len(scan.SAOPKeys) != tc.wantN {
				t.Fatalf("%s: SAOPKeys has %d elements, want %d", tc.sql, len(scan.SAOPKeys), tc.wantN)
			}
			if scan.Key != nil || len(scan.Keys) > 0 || scan.LowKey != nil || scan.HighKey != nil {
				t.Fatalf("%s: SAOP probe must not set Key/Keys/LowKey/HighKey", tc.sql)
			}
		})
	}
}

// TestSAOPQ45ShapeMoves is the falsifiable gate's unit proof: the TPC-DS Q45
// SubPlan-1 shape (`i_item_sk IN (2, 3, 5, 7, 11, 13, 17, 19, 23, 29)` over
// the item PK — bench/tpcds/plans-pg/Q45.txt:28 renders
// `Index Cond: (i_item_sk = ANY (...))`) must move Seq→Index.
func TestSAOPQ45ShapeMoves(t *testing.T) {
	c := saopFixture(t)
	node, err := Plan(parseOne(t,
		"SELECT i_item_id FROM item WHERE i_item_sk IN (2, 3, 5, 7, 11, 13, 17, 19, 23, 29)"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	scan := findIndexScan(node)
	if scan == nil {
		t.Fatalf("Q45 shape: want IndexScan, got plan without one")
	}
	if scan.Index == nil || scan.Index.Name != "item_pkey" {
		t.Fatalf("Q45 shape: want probe on item_pkey, got %v", scan.Index)
	}
	if len(scan.SAOPKeys) != 10 {
		t.Fatalf("Q45 shape: SAOPKeys has %d elements, want 10", len(scan.SAOPKeys))
	}
}

// TestSAOPMultiTableRewriteMoves pins the rewrite-pass half: the same shape
// in a multi-table query promotes the matching SeqScan under the join.
// Legacy arm (useLegacyEnumerator): the rewrite pass skips searched trees
// (isSearchedTree — coordinates), so this is pinned where the pass runs,
// exactly like the `=` arm it mirrors.
func TestSAOPMultiTableRewriteMoves(t *testing.T) {
	useLegacyEnumerator(t)
	c := saopFixture(t)
	node, err := Plan(parseOne(t,
		"SELECT i_item_id FROM item, store WHERE i_item_sk = s_sk AND s_sk IN (1, 2)"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// findIndexScan (not planContainsIndexScan): the join may rewrite to
	// an NLI whose probe lives on Inner, which the shared helper does
	// not descend into.
	scan := findIndexScan(node)
	if scan == nil {
		t.Fatalf("multi-table SAOP: want an IndexScan under the join, got none")
	}
	if len(scan.SAOPKeys) != 2 {
		t.Fatalf("multi-table SAOP: SAOPKeys has %d elements, want 2", len(scan.SAOPKeys))
	}
}

// TestSAOPWithConjunctMoves pins the multi-conjunct single-table shape:
// the IN probes (SAOPKeys) while the remaining range conjunct stays as the
// Filter — the same division the `=` rewrite arm performs.
func TestSAOPWithConjunctMoves(t *testing.T) {
	c := saopFixture(t)
	node, err := Plan(parseOne(t,
		"SELECT i_item_id FROM item WHERE i_item_sk IN (2, 3) AND i_flag > 1"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	scan := findIndexScan(node)
	if scan == nil {
		t.Fatalf("IN plus conjunct: want IndexScan, got plan without one")
	}
	if len(scan.SAOPKeys) != 2 {
		t.Fatalf("IN plus conjunct: SAOPKeys has %d elements, want 2", len(scan.SAOPKeys))
	}
}

func TestSAOPUnmovedShapes(t *testing.T) {
	c := saopFixture(t)
	cases := []struct {
		name string
		sql  string
	}{
		// useOr-only gate (ALL semantics are not a union of descents).
		{"NOT IN", "SELECT i_item_id FROM item WHERE i_item_sk NOT IN (2, 3)"},
		{"not-equal ANY", "SELECT i_item_id FROM item WHERE i_item_sk != ANY (ARRAY[2, 3])"},
		{"ALL", "SELECT i_item_id FROM item WHERE i_item_sk = ALL (ARRAY[2, 3])"},
		{"range ANY", "SELECT i_item_id FROM item WHERE i_item_sk > ANY (ARRAY[2, 3])"},
		// Left-indexkey gate (expression operands have no probe column).
		{"substr IN", "SELECT i_item_id FROM item WHERE substr(i_item_id, 1, 5) IN ('A', 'B')"},
		{"additive IN", "SELECT i_item_id FROM item WHERE i_item_sk + 1 IN (2, 3)"},
		// Const-array gate.
		{"IN subquery", "SELECT i_item_id FROM item WHERE i_item_sk IN (SELECT s_sk FROM store)"},
		{"column element", "SELECT i_item_id FROM item WHERE i_item_sk = ANY (i_flag)"},
		{"mixed const/col", "SELECT i_item_id FROM item WHERE i_item_sk IN (2, i_flag)"},
		// Unindexed column (the TPC-H Q12 bench shape: no index, no probe).
		{"unindexed col", "SELECT i_item_id FROM item WHERE i_flag IN (2, 3)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := Plan(parseOne(t, tc.sql), c)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if planContainsIndexScan(node) {
				t.Fatalf("%s: must stay on SeqScan, got plan with IndexScan", tc.sql)
			}
		})
	}
}

// TestSAOPNumSAScansCost pins the B-14 cost slice in btreeIndexAMCost
// (selfuncs.c:7086-7103, :7718-7719, :7762-7782): the descent charge scales
// with the clamped scan count on the total side only.
func TestSAOPNumSAScansCost(t *testing.T) {
	cp := defaultCostParams()
	base := indexScanInputs{
		relPages: 100, relTuples: 10000,
		indexPages: 90, indexTuples: 10000, treeHeight: 2,
		selectivity: 0.01, correlation: 0, totalTablePages: 200,
	}
	descent := float64(base.treeHeight+1) * pageCPUMultiplier * cp.cpuOperatorCost

	single := costIndexScan(cp, base)
	if single.Startup != descent {
		t.Fatalf("single-descent startup = %v, want descent %v", single.Startup, descent)
	}

	// Unset (0) and 1 both mean a single descent: identical costs.
	for _, n := range []float64{0, 1} {
		in := base
		in.numSAScans = n
		got := costIndexScan(cp, in)
		if got != single {
			t.Fatalf("numSAScans=%v cost = %+v, want single-descent %+v", n, got, single)
		}
	}

	// 10 descents: startup unchanged (first descent), total up by 9 more.
	in := base
	in.numSAScans = 10
	multi := costIndexScan(cp, in)
	if multi.Startup != single.Startup {
		t.Fatalf("SAOP startup = %v, want single-descent startup %v", multi.Startup, single.Startup)
	}
	if want := single.Total + 9*descent; math.Abs(multi.Total-want) > 1e-9 {
		t.Fatalf("SAOP total = %v, want single total + 9 descents = %v", multi.Total, want)
	}

	// Clamp: 100000 descents on a 90-page index clamp to ceil(90/3) = 30.
	in.numSAScans = 100000
	clamped := costIndexScan(cp, in)
	if want := single.Total + 29*descent; math.Abs(clamped.Total-want) > 1e-9 {
		t.Fatalf("clamped total = %v, want single total + 29 descents = %v", clamped.Total, want)
	}

	// The shared index side feeds the bitmap cost too: the same descent
	// scaling applies there (rule #2 — sibling paths price together).
	bmBase := costBitmapIndexScan(cp, base)
	in.numSAScans = 10
	bmMulti := costBitmapIndexScan(cp, in)
	if want := bmBase.Total + 9*descent; math.Abs(bmMulti.Total-want) > 1e-9 {
		t.Fatalf("bitmap SAOP total = %v, want bitmap base + 9 descents = %v", bmMulti.Total, want)
	}
}
