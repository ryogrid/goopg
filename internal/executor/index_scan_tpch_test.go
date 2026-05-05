package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// newTpchTypeFixture creates a DDL-based context with three tables
// (part, customer, lineitem) with varchar/char/timestamp indexed columns
// and seeds representative rows.
func newTpchTypeFixture(t *testing.T) (*Context, catalog.Catalog, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)

	ddls := []string{
		"CREATE TABLE part (p_partkey int, p_type varchar(25))",
		"CREATE TABLE customer (c_custkey int, c_mktsegment char(10))",
		"CREATE TABLE lineitem (l_linenumber int, l_shipdate timestamp)",
		"CREATE INDEX idx_part_type ON part (p_type)",
		"CREATE INDEX idx_cust_seg ON customer (c_mktsegment)",
		"CREATE INDEX idx_ship ON lineitem (l_shipdate)",
	}
	for _, ddl := range ddls {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("DDL %q: %v", ddl, err)
		}
	}

	// Seed part rows.
	partTbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "part"})
	for i, ptype := range []string{"ECONOMY ANODIZED BRASS", "PROMO BRUSHED STEEL", "STANDARD ANODIZED TIN"} {
		if err := writeHeapRow(ctx, ctx.Catalog.RelFileNode(partTbl), partTbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			{Kind: KindString, String: ptype},
		}); err != nil {
			cleanup()
			t.Fatal(err)
		}
	}

	// Seed customer rows.
	custTbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "customer"})
	for i, seg := range []string{"FURNITURE", "HOUSEHOLD", "MACHINERY"} {
		if err := writeHeapRow(ctx, ctx.Catalog.RelFileNode(custTbl), custTbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			{Kind: KindString, String: seg},
		}); err != nil {
			cleanup()
			t.Fatal(err)
		}
	}

	// Seed lineitem rows with TPC-H-shaped dates.
	litemTbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "lineitem"})
	dates := []time.Time{
		time.Date(1993, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(1995, 9, 15, 0, 0, 0, 0, time.UTC),
		time.Date(1997, 11, 30, 0, 0, 0, 0, time.UTC),
	}
	for i, d := range dates {
		if err := writeHeapRow(ctx, ctx.Catalog.RelFileNode(litemTbl), litemTbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			{Kind: KindTime, Time: d},
		}); err != nil {
			cleanup()
			t.Fatal(err)
		}
	}

	// Backfill indexes.
	for _, idxSQL := range []string{
		"CREATE INDEX idx_part_type2 ON part (p_type)",
		"CREATE INDEX idx_cust_seg2 ON customer (c_mktsegment)",
		"CREATE INDEX idx_ship2 ON lineitem (l_shipdate)",
	} {
		// Use the already-created indexes above; no need to create again.
		_ = idxSQL
	}

	return ctx, ctx.Catalog, cleanup
}

// TestIndexScanVarcharEndToEnd verifies that a SELECT with
// WHERE p_type = 'PROMO BRUSHED STEEL' executes via IndexScan
// (not SeqScan) and returns the correct row.
func TestIndexScanVarcharEndToEnd(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE part (p_partkey int, p_type varchar(25))"); err != nil {
		t.Fatal(err)
	}

	// Insert rows BEFORE creating the index so backfillBTree picks them up.
	partTbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "part"})
	rel := ctx.Catalog.RelFileNode(partTbl)
	for i, ptype := range []string{"ECONOMY ANODIZED BRASS", "PROMO BRUSHED STEEL", "STANDARD ANODIZED TIN"} {
		if err := writeHeapRow(ctx, rel, partTbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			{Kind: KindString, String: ptype},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_ptype ON part (p_type)"); err != nil {
		t.Fatal(err)
	}

	sql := "SELECT p_partkey FROM part WHERE p_type = 'PROMO BRUSHED STEEL'"
	plan := planOne(t, sql, ctx.Catalog)

	// Verify the planner chose IndexScan.
	proj, ok := plan.(*planner.Project)
	if !ok {
		t.Fatalf("root=%T want *planner.Project", plan)
	}
	if _, ok := proj.Child.(*planner.IndexScan); !ok {
		t.Fatalf("child=%T want *planner.IndexScan — varchar index not selected by planner", proj.Child)
	}

	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 2 {
		t.Fatalf("p_partkey=%d want 2 (PROMO BRUSHED STEEL is row 2)", rows[0][0].Int)
	}
}

// TestIndexScanCharEndToEnd verifies that WHERE c_mktsegment = 'FURNITURE'
// executes via IndexScan and returns the correct row.
func TestIndexScanCharEndToEnd(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE customer (c_custkey int, c_mktsegment char(10))"); err != nil {
		t.Fatal(err)
	}

	custTbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "customer"})
	rel := ctx.Catalog.RelFileNode(custTbl)
	for i, seg := range []string{"AUTOMOBILE", "FURNITURE", "MACHINERY"} {
		if err := writeHeapRow(ctx, rel, custTbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			{Kind: KindString, String: seg},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_seg ON customer (c_mktsegment)"); err != nil {
		t.Fatal(err)
	}

	sql := "SELECT c_custkey FROM customer WHERE c_mktsegment = 'FURNITURE'"
	plan := planOne(t, sql, ctx.Catalog)

	proj, ok := plan.(*planner.Project)
	if !ok {
		t.Fatalf("root=%T want *planner.Project", plan)
	}
	if _, ok := proj.Child.(*planner.IndexScan); !ok {
		t.Fatalf("child=%T want *planner.IndexScan — char index not selected", proj.Child)
	}

	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 2 {
		t.Fatalf("c_custkey=%d want 2 (FURNITURE is row 2)", rows[0][0].Int)
	}
}

// TestIndexScanTimestampEndToEnd verifies that WHERE l_shipdate =
// timestamp '1995-09-15' executes via IndexScan and returns the row.
func TestIndexScanTimestampEndToEnd(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE lineitem (l_linenumber int, l_shipdate timestamp)"); err != nil {
		t.Fatal(err)
	}

	litemTbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "lineitem"})
	rel := ctx.Catalog.RelFileNode(litemTbl)
	dates := []time.Time{
		time.Date(1993, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(1995, 9, 15, 0, 0, 0, 0, time.UTC),
		time.Date(1997, 11, 30, 0, 0, 0, 0, time.UTC),
	}
	for i, d := range dates {
		if err := writeHeapRow(ctx, rel, litemTbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			{Kind: KindTime, Time: d},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_ship ON lineitem (l_shipdate)"); err != nil {
		t.Fatal(err)
	}

	sql := "SELECT l_linenumber FROM lineitem WHERE l_shipdate = timestamp '1995-09-15'"
	plan := planOne(t, sql, ctx.Catalog)

	proj, ok := plan.(*planner.Project)
	if !ok {
		t.Fatalf("root=%T want *planner.Project", plan)
	}
	if _, ok := proj.Child.(*planner.IndexScan); !ok {
		t.Fatalf("child=%T want *planner.IndexScan — timestamp index not selected", proj.Child)
	}

	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 2 {
		t.Fatalf("l_linenumber=%d want 2 (1995-09-15 is row 2)", rows[0][0].Int)
	}
}
