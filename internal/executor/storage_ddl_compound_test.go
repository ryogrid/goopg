package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestDDLCompoundTimestampNumericIndex verifies the TPC-H canonical
// (l_shipdate timestamp, l_orderkey numeric) compound index.
// Rows with the same shipdate are discriminated by orderkey.
func TestDDLCompoundTimestampNumericIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE lineitem (id int, shipdate timestamp, orderkey numeric)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "lineitem"})
	rel := ctx.Catalog.RelFileNode(tbl)

	date1995 := time.Date(1995, 1, 15, 0, 0, 0, 0, time.UTC)
	date1996 := time.Date(1996, 3, 1, 0, 0, 0, 0, time.UTC)

	rows := []Row{
		// (id, shipdate, orderkey)
		{{Kind: KindInt, Int: 1}, NewTimeDatum(date1995), {Kind: KindNumeric, Int: 1, Scale: 0}},
		{{Kind: KindInt, Int: 2}, NewTimeDatum(date1995), {Kind: KindNumeric, Int: 2, Scale: 0}},
		{{Kind: KindInt, Int: 3}, NewTimeDatum(date1996), {Kind: KindNumeric, Int: 1, Scale: 0}},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_ship_order ON lineitem (shipdate, orderkey)"); err != nil {
		t.Fatalf("CREATE INDEX (timestamp, numeric): %v", err)
	}

	idx, tree := openIndexTreeForTest(t, ctx, "idx_ship_order")

	// Probe: (1995-01-15, 2) — should find exactly 1 row.
	probeKey := indexProbeMultiForTest(t, ctx, idx, NewTimeDatum(date1995), NewNumericInt64Datum(2, 0))
	_, found, err := tree.Search(probeKey)
	if err != nil || !found {
		t.Fatalf("compound probe (1995-01-15, 2): found=%v err=%v", found, err)
	}

	// Range scan for all rows with shipdate in [1995-01-15, 1995-01-15]: a
	// leading-attribute prefix, widened by the engine's own upper-bound funnel
	// (0xFF padding under the blob format, a truncated pivot under the tuple
	// format — see compositeUpperBound).
	loKey := indexProbeMultiForTest(t, ctx, idx, NewTimeDatum(date1995))
	hiKey := ctx.compositeUpperBound(idx, loKey)
	count := 0
	if err := tree.RangeScan(loKey, hiKey, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		count++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if count != 2 {
		t.Fatalf("range scan for 1995-01-15 rows: got %d, want 2", count)
	}
}

// TestDDLCompoundCharNumericIndex verifies the TPC-H
// (c_mktsegment char(10), c_custkey numeric) compound index.
// Padded and unpadded char values collapse to the same key prefix.
func TestDDLCompoundCharNumericIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE customer (id int, mktseg char(10), custkey numeric)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "customer"})
	rel := ctx.Catalog.RelFileNode(tbl)

	rows := []Row{
		{
			{Kind: KindInt, Int: 1},
			NewStringDatum("BUILDING  "), // padded char(10)
			{Kind: KindNumeric, Int: 5, Scale: 0},
		},
		{
			{Kind: KindInt, Int: 2},
			NewStringDatum("BUILDING  "),
			{Kind: KindNumeric, Int: 7, Scale: 0},
		},
		{
			{Kind: KindInt, Int: 3},
			NewStringDatum("FURNITURE "),
			{Kind: KindNumeric, Int: 1, Scale: 0},
		},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_seg_cust ON customer (mktseg, custkey)"); err != nil {
		t.Fatalf("CREATE INDEX (char, numeric): %v", err)
	}

	idx, tree := openIndexTreeForTest(t, ctx, "idx_seg_cust")

	// Probe with unpadded "BUILDING" — should find the custkey=5 row
	// because trim collapses "BUILDING  " and "BUILDING" to the same prefix.
	probeKey := indexProbeMultiForTest(t, ctx, idx, NewStringDatum("BUILDING"), NewNumericInt64Datum(5, 0))
	_, found, err := tree.Search(probeKey)
	if err != nil || !found {
		t.Fatalf("compound probe (BUILDING, 5): found=%v err=%v", found, err)
	}

	// Total rows in index = 3: a leading-attribute range that spans every
	// segment present, widened by the engine's upper-bound funnel.
	totalCount := 0
	if err := tree.RangeScan(
		indexProbeMultiForTest(t, ctx, idx, NewStringDatum("")),
		ctx.compositeUpperBound(idx, indexProbeMultiForTest(t, ctx, idx, NewStringDatum("ZZZZZ"))),
		func(_ []byte, _ storage.ItemPointer) (bool, error) {
			totalCount++
			return true, nil
		},
	); err != nil {
		t.Fatalf("full range scan: %v", err)
	}
	if totalCount != 3 {
		t.Fatalf("full range scan: got %d rows, want 3", totalCount)
	}
}

// TestDDLCompoundVarcharNumericIndex verifies (p_type varchar(25),
// p_partkey numeric) — the TPC-H part table index shape.
func TestDDLCompoundVarcharNumericIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE part (id int, ptype varchar(25), partkey numeric)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "part"})
	rel := ctx.Catalog.RelFileNode(tbl)

	rows := []Row{
		{{Kind: KindInt, Int: 1}, NewStringDatum("ECONOMY ANODIZED BRASS"), {Kind: KindNumeric, Int: 10, Scale: 0}},
		{{Kind: KindInt, Int: 2}, NewStringDatum("ECONOMY ANODIZED BRASS"), {Kind: KindNumeric, Int: 20, Scale: 0}},
		{{Kind: KindInt, Int: 3}, NewStringDatum("PROMO BRUSHED STEEL"), {Kind: KindNumeric, Int: 5, Scale: 0}},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_type_part ON part (ptype, partkey)"); err != nil {
		t.Fatalf("CREATE INDEX (varchar, numeric): %v", err)
	}

	idx, tree := openIndexTreeForTest(t, ctx, "idx_type_part")

	probeKey := indexProbeMultiForTest(t, ctx, idx,
		NewStringDatum("ECONOMY ANODIZED BRASS"), NewNumericInt64Datum(20, 0))
	_, found, err := tree.Search(probeKey)
	if err != nil || !found {
		t.Fatalf("compound probe (ECONOMY ANODIZED BRASS, 20): found=%v err=%v", found, err)
	}
}

// TestDDLCompoundThreeColumnIndex verifies a 3-column
// (o_orderdate timestamp, o_custkey numeric, o_orderkey numeric) index.
func TestDDLCompoundThreeColumnIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE orders (id int, orderdate timestamp, custkey numeric, orderkey numeric)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "orders"})
	rel := ctx.Catalog.RelFileNode(tbl)

	d1 := time.Date(1996, 1, 2, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(1996, 12, 1, 0, 0, 0, 0, time.UTC)

	rows := []Row{
		{{Kind: KindInt, Int: 1}, NewTimeDatum(d1), {Kind: KindNumeric, Int: 370, Scale: 0}, {Kind: KindNumeric, Int: 1, Scale: 0}},
		{{Kind: KindInt, Int: 2}, NewTimeDatum(d1), {Kind: KindNumeric, Int: 370, Scale: 0}, {Kind: KindNumeric, Int: 2, Scale: 0}},
		{{Kind: KindInt, Int: 3}, NewTimeDatum(d2), {Kind: KindNumeric, Int: 100, Scale: 0}, {Kind: KindNumeric, Int: 1, Scale: 0}},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_ord3 ON orders (orderdate, custkey, orderkey)"); err != nil {
		t.Fatalf("CREATE INDEX (timestamp, numeric, numeric): %v", err)
	}

	idx, tree := openIndexTreeForTest(t, ctx, "idx_ord3")

	// Probe: (1996-01-02, 370, 2) — should hit row id=2.
	probeKey := indexProbeMultiForTest(t, ctx, idx,
		NewTimeDatum(d1), NewNumericInt64Datum(370, 0), NewNumericInt64Datum(2, 0))
	_, found, err := tree.Search(probeKey)
	if err != nil || !found {
		t.Fatalf("3-column compound probe: found=%v err=%v", found, err)
	}

	// Index should contain all 3 rows.
	count := 0
	if err := tree.RangeScan(
		indexProbeMultiForTest(t, ctx, idx, NewTimeDatum(time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC))),
		ctx.compositeUpperBound(idx,
			indexProbeMultiForTest(t, ctx, idx, NewTimeDatum(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))),
		func(_ []byte, _ storage.ItemPointer) (bool, error) {
			count++
			return true, nil
		},
	); err != nil {
		t.Fatalf("full range scan: %v", err)
	}
	if count != 3 {
		t.Fatalf("3-column index: got %d rows, want 3", count)
	}
}
