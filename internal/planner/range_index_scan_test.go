package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// rangeIndexCatalog creates a catalog with tables suitable for range
// index scan tests: lineitem with a timestamp-indexed l_shipdate column,
// and a part table with a varchar-indexed p_type column.
func rangeIndexCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()

	// lineitem: l_linenumber int, l_shipdate timestamp
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

	// part: p_partkey int, p_type varchar
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

	return c
}

// TestPlanRangeScanSingleLowerBound verifies that a single >= predicate on an
// indexed timestamp column produces Filter(IndexScan{LowKey!=nil, HighKey=nil}).
func TestPlanRangeScanSingleLowerBound(t *testing.T) {
	cat := rangeIndexCatalog(t)
	stmt := parseOne(t, "SELECT l_linenumber FROM lineitem WHERE l_shipdate >= timestamp '1994-01-01'")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	// M0134-0001 S4: a single-conjunct range WHERE is fully and exactly
	// implemented by the (now exclusive-capable) index scan, so no redundant
	// Filter wraps it.
	idx, ok := proj.Child.(*IndexScan)
	if !ok {
		t.Fatalf("child=%T want *IndexScan without Filter for a single range conjunct", proj.Child)
	}
	if idx.Key != nil {
		t.Fatalf("IndexScan.Key should be nil for range scan, got %T", idx.Key)
	}
	if idx.LowKey == nil {
		t.Fatal("IndexScan.LowKey should be non-nil for >= predicate")
	}
	if idx.HighKey != nil {
		t.Fatalf("IndexScan.HighKey should be nil for single lower bound, got %T", idx.HighKey)
	}
	if idx.LowOp != parser.OpGe {
		t.Fatalf("IndexScan.LowOp should preserve the original >= op, got %v", idx.LowOp)
	}
}

// TestPlanRangeScanTwoSidedTimestamp verifies that a two-sided range on an
// indexed timestamp column produces Filter(IndexScan{LowKey!=nil, HighKey!=nil}).
func TestPlanRangeScanTwoSidedTimestamp(t *testing.T) {
	cat := rangeIndexCatalog(t)
	stmt := parseOne(t, "SELECT l_linenumber FROM lineitem WHERE l_shipdate >= timestamp '1994-01-01' AND l_shipdate < timestamp '1995-01-01'")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	f, ok := proj.Child.(*Filter)
	if !ok {
		t.Fatalf("child=%T want *Filter(IndexScan)", proj.Child)
	}
	idx, ok := f.Child.(*IndexScan)
	if !ok {
		t.Fatalf("Filter.Child=%T want *IndexScan", f.Child)
	}
	if idx.Key != nil {
		t.Fatalf("IndexScan.Key should be nil for range scan, got %T", idx.Key)
	}
	if idx.LowKey == nil {
		t.Fatal("IndexScan.LowKey should be non-nil for >= predicate")
	}
	if idx.HighKey == nil {
		t.Fatal("IndexScan.HighKey should be non-nil for < predicate")
	}
}

// TestPlanRangeScanFallbackNoIndex verifies that a range predicate on a
// non-indexed column falls back to Filter(SeqScan).
func TestPlanRangeScanFallbackNoIndex(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "events"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "ts", Type: catalog.Type{Name: "timestamp"}, NotNull: false},
	}); err != nil {
		t.Fatal(err)
	}
	// No index created on ts
	stmt := parseOne(t, "SELECT id FROM events WHERE ts >= timestamp '1994-01-01'")
	node, err := Plan(stmt, c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	f, ok := proj.Child.(*Filter)
	if !ok {
		t.Fatalf("child=%T want *Filter(SeqScan), got %T", proj.Child, proj.Child)
	}
	if _, ok := f.Child.(*SeqScan); !ok {
		t.Fatalf("Filter.Child=%T want *SeqScan (no index available)", f.Child)
	}
}

// TestPlanRangeScanVarchar verifies that a > predicate on a varchar column
// with a btree index produces Filter(IndexScan{LowKey!=nil, HighKey=nil}).
func TestPlanRangeScanVarchar(t *testing.T) {
	cat := rangeIndexCatalog(t)
	stmt := parseOne(t, "SELECT p_partkey FROM part WHERE p_type > 'A'")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	// M0134-0001 S4: single-conjunct range WHERE → no redundant Filter.
	idx, ok := proj.Child.(*IndexScan)
	if !ok {
		t.Fatalf("child=%T want *IndexScan without Filter for a single range conjunct", proj.Child)
	}
	if idx.Key != nil {
		t.Fatalf("IndexScan.Key should be nil for range scan, got %T", idx.Key)
	}
	if idx.LowKey == nil {
		t.Fatal("IndexScan.LowKey should be non-nil for > predicate")
	}
	if idx.HighKey != nil {
		t.Fatalf("IndexScan.HighKey should be nil for single lower bound, got %T", idx.HighKey)
	}
	if idx.LowOp != parser.OpGt {
		t.Fatalf("IndexScan.LowOp should preserve the original > op, got %v", idx.LowOp)
	}
}

func TestPlanRangeScanRecordsStrictOpAndDropsSingleFilter(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "c1", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "c2", Type: catalog.Type{Name: "int4"}, NotNull: false},
		{Name: "c3", Type: catalog.Type{Name: "varchar"}, NotNull: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "t_c2"}, tbl, []string{"c2"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	// Single strict conjunct: HighOp=OpLt recorded, redundant Filter dropped.
	stmt := parseOne(t, "SELECT c1 FROM t WHERE c2 < 100")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	idx, ok := proj.Child.(*IndexScan)
	if !ok {
		t.Fatalf("child=%T want *IndexScan without Filter for a single range conjunct", proj.Child)
	}
	if idx.HighKey == nil {
		t.Fatal("IndexScan.HighKey should be non-nil for c2 < 100")
	}
	if idx.LowKey != nil {
		t.Fatalf("IndexScan.LowKey should be nil for a single high bound, got %T", idx.LowKey)
	}
	if idx.HighOp != parser.OpLt {
		t.Fatalf("IndexScan.HighOp should preserve the original < op, got %v", idx.HighOp)
	}
	if idx.LowOp != parser.OpUnknown {
		t.Fatalf("IndexScan.LowOp should stay zero (inclusive) with no low bound, got %v", idx.LowOp)
	}

	// Multi-conjunct WHERE: the range conjunct is absorbed but the Filter MUST
	// stay — it still re-checks the non-range conjunct (c3 = 'x').
	stmt2 := parseOne(t, "SELECT c1 FROM t WHERE c2 < 100 AND c3 = 'x'")
	node2, err := Plan(stmt2, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	proj2, ok := node2.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node2)
	}
	f2, ok := proj2.Child.(*Filter)
	if !ok {
		t.Fatalf("child=%T want *Filter(IndexScan) for a multi-conjunct WHERE", proj2.Child)
	}
	idx2, ok := f2.Child.(*IndexScan)
	if !ok {
		t.Fatalf("Filter.Child=%T want *IndexScan", f2.Child)
	}
	if idx2.HighOp != parser.OpLt {
		t.Fatalf("IndexScan.HighOp should preserve < in the multi-conjunct case too, got %v", idx2.HighOp)
	}
	if idx2.HighKey == nil {
		t.Fatal("IndexScan.HighKey should be non-nil for c2 < 100 AND ...")
	}
}
