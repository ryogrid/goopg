package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// compositeIndexCatalog mirrors HammerDB TPC-H's composite primary
// keys: PARTSUPP(PS_PARTKEY, PS_SUPPKEY) and the composite supplementary
// index LINEITEM_PART_SUPP_FKIDX(L_PARTKEY, L_SUPPKEY).
func compositeIndexCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()

	psTbl, err := c.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_pkey"},
		psTbl, []string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}

	litemTbl, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_orderkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "lineitem_part_supp_fkidx"},
		litemTbl, []string{"l_partkey", "l_suppkey"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestCompositeIndexLeadingColumnEqualityPicked: a query
// `WHERE ps_partkey = N` against a table whose only B-tree index is the
// composite (ps_partkey, ps_suppkey) PK must produce an IndexScan, not
// a SeqScan. M0053-0001 DoD.
func TestCompositeIndexLeadingColumnEqualityPicked(t *testing.T) {
	cat := compositeIndexCatalog(t)
	stmt := parseOne(t, "SELECT ps_partkey FROM partsupp WHERE ps_partkey = 42")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !planContainsIndexScan(node) {
		t.Fatalf("expected IndexScan in plan, got tree without one: %T", node)
	}
}

// TestCompositeIndexNonLeadingColumnFallsBack: a query on the trailing
// column (`ps_suppkey = N`) cannot use the composite index — only the
// leading column is reachable via prefix probe. The planner must fall
// back to SeqScan.
func TestCompositeIndexNonLeadingColumnFallsBack(t *testing.T) {
	cat := compositeIndexCatalog(t)
	stmt := parseOne(t, "SELECT ps_partkey FROM partsupp WHERE ps_suppkey = 7")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if planContainsIndexScan(node) {
		t.Fatalf("non-leading column should NOT use composite index: tree=%T", node)
	}
}

// TestCompositeIndexLeadingColumnRangePicked: range predicates on the
// leading column also flow through tryRangeIndexScan and should be
// converted to a Filter(IndexScan{LowKey,HighKey}).
func TestCompositeIndexLeadingColumnRangePicked(t *testing.T) {
	cat := compositeIndexCatalog(t)
	stmt := parseOne(t, "SELECT l_orderkey FROM lineitem WHERE l_partkey >= 100 AND l_partkey < 200")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !planContainsIndexScan(node) {
		t.Fatalf("expected IndexScan in plan for range on leading column, got: %T", node)
	}
}

// TestSingleColumnIndexPreferredOverComposite: when both a single-column
// and a composite index exist on the same leading column, the planner
// should prefer the single-column one (cheaper exact-equality probe).
func TestSingleColumnIndexPreferredOverComposite(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "b", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Composite index registered first, single-column second.
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "t_ab_idx"}, tbl, []string{"a", "b"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "t_a_idx"}, tbl, []string{"a"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	idx := findBTreeIndexForColumn(cat, tbl, "a")
	if idx == nil {
		t.Fatal("findBTreeIndexForColumn returned nil")
	}
	if len(idx.Columns) != 1 {
		t.Errorf("expected single-column index preferred, got %d columns (%s)", len(idx.Columns), idx.Name)
	}
}

// planContainsIndexScan walks the plan tree looking for any IndexScan
// or IndexOnlyScan node (the latter is a planner-level promotion of the
// former by tryPromoteIndexOnlyScan when the projected columns are
// fully covered by the index key).
func planContainsIndexScan(n Node) bool {
	if n == nil {
		return false
	}
	switch v := n.(type) {
	case *IndexScan:
		return true
	case *IndexOnlyScan:
		return true
	case *Project:
		return planContainsIndexScan(v.Child)
	case *Filter:
		return planContainsIndexScan(v.Child)
	case *Sort:
		return planContainsIndexScan(v.Child)
	case *Limit:
		return planContainsIndexScan(v.Child)
	}
	return false
}
