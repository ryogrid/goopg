package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// newTypesIndexCatalog creates a catalog with three tables — one with a
// varchar column, one with a char column, one with a timestamp column —
// each having a btree index, ready for M0044-0005 planner tests.
func newTypesIndexCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()

	// varchar table: part (p_partkey int, p_type varchar(25))
	partTbl, err := c.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "p_type", Type: catalog.Type{Name: "varchar"}, NotNull: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "idx_part_type"}, partTbl, []string{"p_type"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	// char table: customer (c_custkey int, c_mktsegment char(10))
	custTbl, err := c.CreateTable(parser.ObjectName{Name: "customer"}, []catalog.Column{
		{Name: "c_custkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "c_mktsegment", Type: catalog.Type{Name: "char"}, NotNull: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "idx_cust_seg"}, custTbl, []string{"c_mktsegment"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	// timestamp table: lineitem (l_linenumber int, l_shipdate timestamp)
	litemTbl, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_linenumber", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_shipdate", Type: catalog.Type{Name: "timestamp"}, NotNull: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "idx_lineitem_ship"}, litemTbl, []string{"l_shipdate"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	return c
}

// TestPlanVarcharColumnPicksIndexScan verifies that a WHERE predicate
// on a varchar column with a btree index produces IndexScan rather
// than Filter(SeqScan). This is the M0044-0005 planner gate.
func TestPlanVarcharColumnPicksIndexScan(t *testing.T) {
	cat := newTypesIndexCatalog(t)
	stmt := parseOne(t, "SELECT p_partkey FROM part WHERE p_type = 'ECONOMY ANODIZED BRASS'")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	if _, ok := proj.Child.(*IndexScan); !ok {
		t.Fatalf("child=%T want *IndexScan (varchar index not selected)", proj.Child)
	}
}

// TestPlanCharColumnPicksIndexScan verifies that a WHERE predicate on
// a char column with a btree index produces IndexScan.
func TestPlanCharColumnPicksIndexScan(t *testing.T) {
	cat := newTypesIndexCatalog(t)
	stmt := parseOne(t, "SELECT c_custkey FROM customer WHERE c_mktsegment = 'FURNITURE'")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	if _, ok := proj.Child.(*IndexScan); !ok {
		t.Fatalf("child=%T want *IndexScan (char index not selected)", proj.Child)
	}
}

// TestPlanTimestampColumnPicksIndexScan verifies that a WHERE predicate
// on a timestamp column with a typed literal picks IndexScan.
func TestPlanTimestampColumnPicksIndexScan(t *testing.T) {
	cat := newTypesIndexCatalog(t)
	stmt := parseOne(t, "SELECT l_linenumber FROM lineitem WHERE l_shipdate = timestamp '1995-01-01'")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	if _, ok := proj.Child.(*IndexScan); !ok {
		t.Fatalf("child=%T want *IndexScan (timestamp index not selected)", proj.Child)
	}
}

// TestPlanNoIndexFallsBackToSeqScan verifies that without an index, the
// planner still falls back to Filter(SeqScan) for varchar predicates.
func TestPlanNoIndexFallsBackToSeqScan(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "noindex"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "tag", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	stmt := parseOne(t, "SELECT id FROM noindex WHERE tag = 'foo'")
	node, err := Plan(stmt, c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Should be Project(Filter(SeqScan)), not IndexScan.
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	if _, ok := proj.Child.(*IndexScan); ok {
		t.Fatalf("child=*IndexScan (no index registered, should be SeqScan path)")
	}
}
