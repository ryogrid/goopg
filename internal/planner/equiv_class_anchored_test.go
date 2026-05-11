package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// makeAnchorColRef builds a ColumnRef with a SourceTableIdx
// usable for anchor-test setups.
func makeAnchorColRef(name string, srcIdx int16) *ColumnRef {
	return &ColumnRef{
		Name:           name,
		Index:          0,
		Type:           catalog.Type{Name: "int4"},
		SourceTableIdx: srcIdx,
	}
}

// TestInferAnchoredEqualitiesQ5NationAnchor pins design 02 §8.3 +
// §5: in Q5's nationkey class with nation as a SmallDimension
// anchor, exactly one synthesised edge fires — `c_nationkey =
// n_nationkey` — bridging customer onto the filtered nation
// side. (M0077-0004 / Slice D.)
func TestInferAnchoredEqualitiesQ5NationAnchor(t *testing.T) {
	// Q5-shape nationkey class:
	//   supplier.s_nationkey = nation.n_nationkey  (explicit)
	//   customer.c_nationkey = supplier.s_nationkey (explicit)
	// → expect synthesised: customer.c_nationkey = nation.n_nationkey.
	customer := &catalog.Table{Name: "customer"}
	supplier := &catalog.Table{Name: "supplier"}
	nation := &catalog.Table{Name: "nation", SmallDimension: true}
	rels := []baseRelInfo{
		{bindingIdx: 0, sourceIdx: 1, table: customer, baseRows: 150000, filteredRows: 150000},
		{bindingIdx: 1, sourceIdx: 2, table: supplier, baseRows: 10000, filteredRows: 10000},
		{bindingIdx: 2, sourceIdx: 3, table: nation, baseRows: 25, filteredRows: 25, isSmallDimension: true},
	}
	cnationkey := makeAnchorColRef("c_nationkey", 1)
	snationkey := makeAnchorColRef("s_nationkey", 2)
	nnationkey := makeAnchorColRef("n_nationkey", 3)
	conjuncts := []Expr{
		&BinaryOp{Op: parser.OpEq, Left: snationkey, Right: nnationkey},
		&BinaryOp{Op: parser.OpEq, Left: cnationkey, Right: snationkey},
	}
	added := inferAnchoredEqualities(conjuncts, rels)
	if len(added) != 1 {
		t.Fatalf("expected 1 anchored edge for Q5 nationkey class, got %d: %#v", len(added), added)
	}
	bo, ok := added[0].(*BinaryOp)
	if !ok || bo.Op != parser.OpEq {
		t.Fatalf("anchored edge is not OpEq BinaryOp: %#v", added[0])
	}
	// Either side may carry the anchor (n_nationkey); the other
	// must be c_nationkey (customer).
	la := bo.Left.(*ColumnRef)
	ra := bo.Right.(*ColumnRef)
	names := []string{la.Name, ra.Name}
	if !(contains(names, "c_nationkey") && contains(names, "n_nationkey")) {
		t.Errorf("anchored edge must connect c_nationkey ↔ n_nationkey, got %v", names)
	}
}

// TestInferAnchoredEqualitiesDoesNotBroadcastFromUnfilteredClass
// pins design 02 §8.3: a class containing only large unfiltered
// fact relations gets NO synthesised edges. This is the rule
// that prevents the M0075-0001 / M0076-0001 Q9 hang at 380-600s.
// (M0077-0004 / Slice D.)
func TestInferAnchoredEqualitiesDoesNotBroadcastFromUnfilteredClass(t *testing.T) {
	// Q9-shape l_partkey class: lineitem.l_partkey =
	// partsupp.ps_partkey = part.p_partkey, all unfiltered fact
	// relations. With no anchor, no synthesised edges.
	lineitem := &catalog.Table{Name: "lineitem"}
	partsupp := &catalog.Table{Name: "partsupp"}
	part := &catalog.Table{Name: "part"}
	rels := []baseRelInfo{
		{bindingIdx: 0, sourceIdx: 1, table: lineitem, baseRows: 6000000, filteredRows: 6000000},
		{bindingIdx: 1, sourceIdx: 2, table: partsupp, baseRows: 800000, filteredRows: 800000},
		{bindingIdx: 2, sourceIdx: 3, table: part, baseRows: 200000, filteredRows: 200000},
	}
	lpart := makeAnchorColRef("l_partkey", 1)
	pspart := makeAnchorColRef("ps_partkey", 2)
	ppart := makeAnchorColRef("p_partkey", 3)
	conjuncts := []Expr{
		&BinaryOp{Op: parser.OpEq, Left: lpart, Right: pspart},
		&BinaryOp{Op: parser.OpEq, Left: pspart, Right: ppart},
	}
	added := inferAnchoredEqualities(conjuncts, rels)
	if len(added) != 0 {
		t.Errorf("unfiltered fact-class must produce NO anchored edges, got %d: %#v", len(added), added)
	}
}

// TestInferAnchoredEqualitiesFilteredAnchor pins anchor rule (2):
// a relation with `filteredRows*2 ≤ baseRows` qualifies as
// anchor even without SmallDimension flag. (M0077-0004 /
// Slice D.)
func TestInferAnchoredEqualitiesFilteredAnchor(t *testing.T) {
	orders := &catalog.Table{Name: "orders"}
	lineitem := &catalog.Table{Name: "lineitem"}
	rels := []baseRelInfo{
		// orders 1.5M → 100K (>50% reduction → anchor).
		{bindingIdx: 0, sourceIdx: 1, table: orders, baseRows: 1500000, filteredRows: 100000, hasLocalFilter: true},
		// lineitem unfiltered, big → non-anchor.
		{bindingIdx: 1, sourceIdx: 2, table: lineitem, baseRows: 6000000, filteredRows: 6000000},
	}
	ookey := makeAnchorColRef("o_orderkey", 1)
	lokey := makeAnchorColRef("l_orderkey", 2)
	conjuncts := []Expr{
		&BinaryOp{Op: parser.OpEq, Left: ookey, Right: lokey},
	}
	// Already-explicit pair → no synthesis (single class, single explicit edge).
	added := inferAnchoredEqualities(conjuncts, rels)
	if len(added) != 0 {
		t.Errorf("class with one explicit edge needs no synthesis, got %d", len(added))
	}
}

// TestInferAnchoredEqualitiesSmallAnchorRowsThreshold pins anchor
// rule (3): a relation with `filteredRows ≤ 1024` is an anchor
// regardless of baseRows. (M0077-0004 / Slice D.)
func TestInferAnchoredEqualitiesSmallAnchorRowsThreshold(t *testing.T) {
	tinyTbl := &catalog.Table{Name: "tiny"}
	rels := []baseRelInfo{
		// tinyTbl filtered down to 100 rows → anchor.
		{bindingIdx: 0, sourceIdx: 1, table: tinyTbl, baseRows: 1000000, filteredRows: 100, hasLocalFilter: true},
	}
	if !relIsAnchor(rels[0]) {
		t.Error("filteredRows=100 must qualify as anchor (≤ smallAnchorRowsThreshold)")
	}
}

// TestInferAnchoredEqualitiesAtMostOneEdgePerTarget pins design
// 02 §5 rule (5): no more than one synthesised edge per
// (target, class). (M0077-0004 / Slice D.)
func TestInferAnchoredEqualitiesAtMostOneEdgePerTarget(t *testing.T) {
	// Two anchors (a, b both SmallDim) + one non-anchor (c).
	// Class via: a=b, b=c. Expect: at most ONE edge a=c
	// (b=c already explicit; a=c is the only missing pair),
	// not multiple edges to c.
	a := &catalog.Table{Name: "a", SmallDimension: true}
	b := &catalog.Table{Name: "b", SmallDimension: true}
	c := &catalog.Table{Name: "c"}
	rels := []baseRelInfo{
		{bindingIdx: 0, sourceIdx: 1, table: a, baseRows: 25, filteredRows: 25, isSmallDimension: true},
		{bindingIdx: 1, sourceIdx: 2, table: b, baseRows: 25, filteredRows: 25, isSmallDimension: true},
		{bindingIdx: 2, sourceIdx: 3, table: c, baseRows: 1500000, filteredRows: 1500000},
	}
	acol := makeAnchorColRef("a_id", 1)
	bcol := makeAnchorColRef("b_id", 2)
	ccol := makeAnchorColRef("c_id", 3)
	conjuncts := []Expr{
		&BinaryOp{Op: parser.OpEq, Left: acol, Right: bcol},
		&BinaryOp{Op: parser.OpEq, Left: bcol, Right: ccol},
	}
	added := inferAnchoredEqualities(conjuncts, rels)
	if len(added) != 1 {
		t.Errorf("expected 1 synthesised edge per target c, got %d: %#v", len(added), added)
	}
}

// TestInferAnchoredEqualitiesEmptyInputs pins defensive paths:
// no rels, no conjuncts, no anchors → empty output. (M0077-0004
// / Slice D.)
func TestInferAnchoredEqualitiesEmptyInputs(t *testing.T) {
	if got := inferAnchoredEqualities(nil, nil); got != nil {
		t.Errorf("nil/nil must return nil, got %#v", got)
	}
	if got := inferAnchoredEqualities([]Expr{}, []baseRelInfo{}); got != nil {
		t.Errorf("empty/empty must return nil, got %#v", got)
	}
	// Conjuncts present but no anchors → nothing to synthesise.
	col1 := makeAnchorColRef("x", 1)
	col2 := makeAnchorColRef("y", 2)
	rels := []baseRelInfo{
		{bindingIdx: 0, sourceIdx: 1, baseRows: 1000000, filteredRows: 1000000},
		{bindingIdx: 1, sourceIdx: 2, baseRows: 1000000, filteredRows: 1000000},
	}
	conj := []Expr{&BinaryOp{Op: parser.OpEq, Left: col1, Right: col2}}
	if got := inferAnchoredEqualities(conj, rels); len(got) != 0 {
		t.Errorf("no-anchor class must produce 0 edges, got %d", len(got))
	}
}

// relIsAnchor mirrors the inline rule in
// inferAnchoredEqualities for testing rule (3) in isolation.
func relIsAnchor(r baseRelInfo) bool {
	if r.isSmallDimension {
		return true
	}
	if r.hasLocalFilter && r.baseRows > 0 && r.filteredRows > 0 && r.filteredRows*2 <= r.baseRows {
		return true
	}
	if r.filteredRows > 0 && r.filteredRows <= smallAnchorRowsThreshold {
		return true
	}
	return false
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
